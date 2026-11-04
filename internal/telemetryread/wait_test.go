package telemetryread

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/driverforge/cli/internal/driverforgeapi"
	"github.com/driverforge/cli/internal/telemetry"
)

type fakeLive struct {
	pubs   chan json.RawMessage
	states chan telemetry.StreamState
}

func newFakeLive() *fakeLive {
	return &fakeLive{pubs: make(chan json.RawMessage, 64), states: make(chan telemetry.StreamState, 16)}
}

func (l *fakeLive) Publications() <-chan json.RawMessage { return l.pubs }
func (l *fakeLive) States() <-chan telemetry.StreamState { return l.states }
func (l *fakeLive) Close()                               {}

func (l *fakeLive) publish(id string, ms int64, msg string) {
	l.pubs <- json.RawMessage(fmt.Sprintf(`{"type":"log","id":%q,"createdAt":%d,"level":"INFO","message":%q}`, id, ms, msg))
}

func opener(l *fakeLive) Opener {
	return func(context.Context) (Live, error) {
		l.states <- telemetry.Subscribed
		return l, nil
	}
}

func TestWaitSatisfiedByBackfillReturnsWithoutWaiting(t *testing.T) {
	src := &fakeSource{}
	src.add(logAt("l1", 1000, "booting"), logAt("l2", 2000, `{"data":1}`), logAt("l3", 3000, "after"))
	res, err := Wait{
		Source: src, Open: opener(newFakeLive()), Tail: 100,
		UntilMatch: regexp.MustCompile(`"data"`), Timeout: time.Minute,
	}.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Satisfied || res.Reason != ReasonUntilMatch || res.Transport != TransportSubscription {
		t.Errorf("result = %+v", res)
	}
	if got := ids(res.Entries); len(got) != 2 || got[1] != "l2" {
		t.Errorf("entries = %v, want up to and including the match", got)
	}
	if res.Elapsed > time.Second {
		t.Errorf("waited %s for a condition the backfill already met", res.Elapsed)
	}
}

func TestWaitDeduplicatesBackfillAndLive(t *testing.T) {
	src := &fakeSource{}
	src.add(logAt("l1", 1000, "a"))
	live := newFakeLive()
	// l1 lands between subscribing and the backfill: it is in both.
	live.publish("l1", 1000, "a")
	live.publish("l2", 2000, "b")
	res, err := Wait{Source: src, Open: opener(live), Tail: 100, MinEvents: 2, Timeout: 5 * time.Second}.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(res.Entries); len(got) != 2 || got[0] != "l1" || got[1] != "l2" || res.Reason != ReasonMinEvents {
		t.Errorf("entries = %v reason %q, want l1 once then l2", got, res.Reason)
	}
}

func TestWaitLiveMatchReturnsPromptly(t *testing.T) {
	live := newFakeLive()
	go func() {
		time.Sleep(50 * time.Millisecond)
		live.publish("l9", 9000, "Vendor response {\"data\":{}}")
	}()
	start := time.Now()
	res, err := Wait{Source: &fakeSource{}, Open: opener(live), Tail: 100, UntilMatch: regexp.MustCompile(`"data"`), Timeout: 30 * time.Second}.Run(context.Background())
	if err != nil || !res.Satisfied {
		t.Fatalf("res %+v err %v", res, err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("returned %s after the match arrived", time.Since(start))
	}
}

func TestWaitTimeoutIsAResultNotAnError(t *testing.T) {
	src := &fakeSource{}
	src.add(logAt("l1", 1000, "a"))
	res, err := Wait{Source: src, Open: opener(newFakeLive()), Tail: 100, UntilMatch: regexp.MustCompile("never"), Timeout: 100 * time.Millisecond}.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Satisfied || res.Reason != ReasonTimeout || len(res.Entries) != 1 {
		t.Errorf("result = %+v, want unsatisfied timeout with the gathered entry", res)
	}
}

func TestWaitInterruptReturnsContextError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	_, err := Wait{Source: &fakeSource{}, Open: opener(newFakeLive()), Tail: 1}.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestWaitReconnectFillsTheGapWithoutDuplicates(t *testing.T) {
	src := &fakeSource{}
	src.add(logAt("l1", 1000, "a"))
	live := newFakeLive()
	var emitted []string
	go func() {
		time.Sleep(30 * time.Millisecond)
		live.states <- telemetry.Lost
		// l2 is published while the subscription is down: only the query API
		// has it. l1 is re-read by the gap fill too.
		src.add(logAt("l2", 2000, "b"))
		live.states <- telemetry.Subscribed
		time.Sleep(30 * time.Millisecond)
		live.publish("l2", 2000, "b") // a late duplicate after resubscribe
		live.publish("l3", 3000, "c")
	}()
	res, err := Wait{
		Source: src, Open: opener(live), Tail: 100, MinEvents: 3, Timeout: 5 * time.Second,
		Emit: func(e Entry) { emitted = append(emitted, e.ID) },
	}.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(res.Entries); fmt.Sprint(got) != "[l1 l2 l3]" || fmt.Sprint(emitted) != "[l1 l2 l3]" {
		t.Errorf("entries %v emitted %v, want each of l1 l2 l3 exactly once", got, emitted)
	}
}

func TestWaitGivesUpAfterSustainedDisconnect(t *testing.T) {
	live := newFakeLive()
	go func() {
		time.Sleep(20 * time.Millisecond)
		live.states <- telemetry.Lost
	}()
	_, err := Wait{Source: &fakeSource{}, Open: opener(live), Tail: 1, LostAfter: 50 * time.Millisecond, Timeout: 10 * time.Second}.Run(context.Background())
	if !errors.Is(err, ErrConnectionLost) {
		t.Errorf("err = %v, want ErrConnectionLost", err)
	}
}

func TestWaitPollsWhenTheSubscriptionIsUnavailable(t *testing.T) {
	src := &fakeSource{}
	go func() {
		time.Sleep(30 * time.Millisecond)
		src.add(logAt("l1", 1000, "arrived"))
	}()
	res, err := Wait{
		Source: src, Tail: 10, MinEvents: 1, Timeout: 5 * time.Second,
		Open:         func(context.Context) (Live, error) { return nil, errors.New("refused") },
		PollInterval: 20 * time.Millisecond,
	}.Run(context.Background())
	if err != nil || !res.Satisfied || res.Transport != TransportPolling {
		t.Errorf("res %+v err %v, want satisfied by polling", res, err)
	}
}

func TestWaitRateLimitBeyondTimeoutFails(t *testing.T) {
	src := &fakeSource{}
	go func() {
		time.Sleep(30 * time.Millisecond)
		src.mu.Lock()
		src.err = &driverforgeapi.RateLimitError{RetryAfter: time.Hour}
		src.mu.Unlock()
	}()
	_, err := Wait{Source: src, Tail: 1, MinEvents: 1, Timeout: 5 * time.Second, PollInterval: 50 * time.Millisecond}.Run(context.Background())
	var rl *driverforgeapi.RateLimitError
	if !errors.As(err, &rl) {
		t.Errorf("err = %v, want the rate limit", err)
	}
}
