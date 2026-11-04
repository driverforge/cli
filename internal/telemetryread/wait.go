package telemetryread

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"regexp"
	"time"

	"github.com/driverforge/cli/internal/driverforgeapi"
	"github.com/driverforge/cli/internal/telemetry"
)

// ErrConnectionLost means the live connection stayed down past Wait.LostAfter.
var ErrConnectionLost = errors.New("telemetryread: live connection lost")

// Transports a wait can use to see new entries.
const (
	TransportSubscription = "subscription" // the web app's live channel
	TransportPolling      = "polling"      // repeated queries, when the channel is unavailable
)

// Stop reasons.
const (
	ReasonUntilMatch = "until_match"
	ReasonMinEvents  = "min_events"
	ReasonTimeout    = "timeout"
)

// Live is an open live subscription; *telemetry.Stream implements it.
type Live interface {
	Publications() <-chan json.RawMessage
	States() <-chan telemetry.StreamState
	Close()
}

// Opener opens the live subscription.
type Opener func(ctx context.Context) (Live, error)

// Wait reads the newest entries and then keeps reading new ones as they arrive,
// until a stop condition is met, the timeout passes, or ctx is cancelled.
//
// It opens the live subscription before running the backfill query and
// de-duplicates by entry id, so an entry that lands between the two is seen
// exactly once. Stop conditions are checked against the backfill too: a
// condition already met returns without waiting.
type Wait struct {
	Source Source
	Open   Opener // nil: poll instead of subscribing
	Names  Names
	Filter Filter
	Tail   int // entries to backfill

	UntilMatch *regexp.Regexp // stop at the first entry matching this
	MinEvents  int            // stop once this many entries have passed the filter
	Timeout    time.Duration  // 0: no limit (follow until cancelled)

	// Emit, when set, receives each accepted entry as it is accepted, in
	// order — for streaming output.
	Emit func(Entry)

	SubscribeTimeout time.Duration // wait this long for the subscription before polling; default 5s
	PollInterval     time.Duration // base poll interval, jittered; default 1s
	LostAfter        time.Duration // give up after this long disconnected; default 60s
}

// WaitResult is what a wait gathered.
type WaitResult struct {
	Entries       []Entry // accepted entries, in acceptance order
	Satisfied     bool
	Reason        string
	Elapsed       time.Duration
	Transport     string
	WindowMinutes int
}

func (w *Wait) defaults() {
	if w.SubscribeTimeout == 0 {
		w.SubscribeTimeout = 5 * time.Second
	}
	if w.PollInterval == 0 {
		w.PollInterval = time.Second
	}
	if w.LostAfter == 0 {
		w.LostAfter = 60 * time.Second
	}
}

// waiter is one run's state.
type waiter struct {
	*Wait
	seen     map[string]bool
	res      WaitResult
	newest   time.Time // newest accepted entry's time: where a gap fill resumes
	started  time.Time
	matching int
	// lostSince is when the live connection dropped; zero while it is up.
	lostSince time.Time
}

// Run performs the wait. It returns the gathered entries even alongside an
// error. Cancelling ctx (an interrupt) returns ctx's error; reaching the
// timeout is not an error but a result with Satisfied false.
func (w Wait) Run(ctx context.Context) (WaitResult, error) {
	w.defaults()
	r := &waiter{Wait: &w, seen: map[string]bool{}, started: time.Now()}
	r.res.Transport = TransportPolling

	var (
		wctx   context.Context
		cancel context.CancelFunc
	)
	if w.Timeout > 0 {
		wctx, cancel = context.WithTimeout(ctx, w.Timeout)
	} else {
		wctx, cancel = context.WithCancel(ctx)
	}
	defer cancel()

	live := r.subscribe(wctx)
	if live != nil {
		defer live.Close()
		r.res.Transport = TransportSubscription
	}
	if stop, err := r.backfill(wctx); err != nil || stop {
		return r.end(ctx, wctx, err)
	}
	return r.follow(ctx, wctx, live)
}

// backfill accepts the newest Tail entries, reporting whether they already
// met a stop condition.
func (r *waiter) backfill(ctx context.Context) (bool, error) {
	res, err := Fetch(ctx, r.Source, Query{Filter: r.Filter, Tail: r.Tail})
	if err != nil {
		return false, err
	}
	r.res.WindowMinutes = res.WindowMinutes
	for _, e := range res.Entries {
		if r.accept(e) {
			return true, nil
		}
	}
	return false, nil
}

// follow accepts new entries from the live subscription, or by polling when
// there is none, until a stop condition, the timeout, an interrupt or an
// error.
func (r *waiter) follow(ctx, wctx context.Context, live Live) (WaitResult, error) {
	var (
		pubs   <-chan json.RawMessage
		states <-chan telemetry.StreamState
		poll   <-chan time.Time
	)
	if live != nil {
		pubs, states = live.Publications(), live.States()
	} else {
		poll = time.After(r.pollDelay())
	}
	watchdog := time.NewTicker(time.Second)
	defer watchdog.Stop()

	for {
		var (
			stop bool
			err  error
		)
		select {
		case <-wctx.Done():
			return r.finish(ctx, wctx, nil)
		case pub := <-pubs:
			stop = r.onPublication(pub)
		case st := <-states:
			stop, err = r.onState(wctx, st)
		case <-watchdog.C:
			err = r.checkLost()
		case <-poll:
			stop, err = r.onPoll(wctx)
			poll = time.After(r.pollDelay())
		}
		if stop || err != nil {
			return r.end(ctx, wctx, err)
		}
	}
}

// end finishes a wait that stopped on a condition (err nil) or an error.
func (r *waiter) end(ctx, wctx context.Context, err error) (WaitResult, error) {
	if err != nil {
		return r.finish(ctx, wctx, err)
	}
	return r.done(), nil
}

func (r *waiter) onPublication(pub json.RawMessage) bool {
	e, ok := FromPublication(pub, r.Names)
	return ok && r.Filter.Match(e) && r.accept(e)
}

// onState tracks the connection. After a reconnect it reads what was
// published while the subscription was down, which never arrives live.
func (r *waiter) onState(ctx context.Context, st telemetry.StreamState) (bool, error) {
	switch {
	case st == telemetry.Lost && r.lostSince.IsZero():
		r.lostSince = time.Now()
	case st == telemetry.Subscribed && !r.lostSince.IsZero():
		r.lostSince = time.Time{}
		return r.catchUp(ctx)
	}
	return false, nil
}

// checkLost fails the wait once the connection has stayed down too long.
func (r *waiter) checkLost() error {
	if !r.lostSince.IsZero() && time.Since(r.lostSince) > r.LostAfter {
		return ErrConnectionLost
	}
	return nil
}

// onPoll reads new entries. A rate limit is waited out when the server's
// retry-after fits in the time left, and is an error otherwise.
func (r *waiter) onPoll(ctx context.Context) (bool, error) {
	stop, err := r.catchUp(ctx)
	var rl *driverforgeapi.RateLimitError
	if errors.As(err, &rl) && r.sleep(ctx, rl.RetryAfter) {
		return false, nil
	}
	return stop, err
}

// subscribe opens the live subscription and waits for it to go live. It
// returns nil when there is no opener or the subscription isn't live within
// SubscribeTimeout, and the wait polls instead.
func (r *waiter) subscribe(ctx context.Context) Live {
	if r.Open == nil {
		return nil
	}
	live, err := r.Open(ctx)
	if err != nil || live == nil {
		return nil
	}
	timeout := time.NewTimer(r.SubscribeTimeout)
	defer timeout.Stop()
	for {
		select {
		case st := <-live.States():
			if st == telemetry.Subscribed {
				return live
			}
		case <-timeout.C:
			live.Close()
			return nil
		case <-ctx.Done():
			live.Close()
			return nil
		}
	}
}

// catchUp reads entries newer than the newest one seen and accepts the unseen
// ones. It reports whether a stop condition was met.
func (r *waiter) catchUp(ctx context.Context) (bool, error) {
	f := r.Filter
	if !r.newest.IsZero() && r.newest.After(f.Since) {
		f.Since = r.newest
	}
	res, err := Fetch(ctx, r.Source, Query{Filter: f, Tail: MaxPageSize})
	if err != nil {
		return false, err
	}
	for _, e := range res.Entries {
		if r.accept(e) {
			return true, nil
		}
	}
	return false, nil
}

// accept records an entry that passed the filter, unless already seen, and
// reports whether it met a stop condition.
func (r *waiter) accept(e Entry) bool {
	key := string(e.Type) + ":" + e.ID
	if r.seen[key] {
		return false
	}
	r.seen[key] = true
	r.res.Entries = append(r.res.Entries, e)
	if e.at.After(r.newest) {
		r.newest = e.at
	}
	r.matching++
	if r.Emit != nil {
		r.Emit(e)
	}
	switch {
	case r.UntilMatch != nil && Matches(r.UntilMatch, e):
		r.res.Satisfied, r.res.Reason = true, ReasonUntilMatch
	case r.MinEvents > 0 && r.matching >= r.MinEvents:
		r.res.Satisfied, r.res.Reason = true, ReasonMinEvents
	default:
		return false
	}
	return true
}

func (r *waiter) done() WaitResult {
	r.res.Elapsed = time.Since(r.started)
	return r.res
}

// finish ends the wait on a cancelled context or an error. An interrupt
// (parent ctx cancelled) returns its error; the timeout alone is a result.
func (r *waiter) finish(parent, wctx context.Context, err error) (WaitResult, error) {
	res := r.done()
	if parent.Err() != nil {
		return res, parent.Err()
	}
	if wctx.Err() != nil && (err == nil || errors.Is(err, context.DeadlineExceeded)) {
		res.Reason = ReasonTimeout
		return res, nil
	}
	return res, err
}

// sleep waits d, reporting false when ctx ends first — including when the
// remaining time is shorter than d, which the caller treats as a failure.
func (r *waiter) sleep(ctx context.Context, d time.Duration) bool {
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) < d {
		return false
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// pollDelay is the poll interval with up to 25% jitter, so many waiting CLIs
// don't poll in step.
func (r *waiter) pollDelay() time.Duration {
	return r.PollInterval + time.Duration(rand.Int64N(int64(r.PollInterval/4)+1))
}
