package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/driverforge/cli/internal/driverforgeapi"
	"github.com/driverforge/cli/internal/telemetry"
	"github.com/driverforge/cli/internal/telemetryread"
)

// runWait is a read with a stop condition or --follow: backfill, then the live
// stream, until the condition is met, the timeout passes or it's interrupted.
// silent is the device_not_reporting warning, when the controller is silent.
func runWait(ctx context.Context, api *driverforgeapi.Client, scope *telemetryScope, src telemetryread.GraphSource, o readOpts, silent *warning, stdout io.Writer, diag diagnostics) error {
	so := scope.out(o.filter.Since, o.filter.Until)
	w := telemetryread.Wait{
		Source: src, Names: scope.names, Filter: o.filter, Tail: o.tail,
		UntilMatch: o.untilMatch, MinEvents: o.minEvents, Timeout: o.timeout,
		Open: func(ctx context.Context) (telemetryread.Live, error) {
			return telemetry.OpenStream(ctx, scope.project.ID, api.CentrifugoToken)
		},
	}
	var f *follower
	if o.follow {
		f = &follower{opts: o, scope: scope, out: stdout}
		diag.scope(so)
		for _, wn := range o.warnings {
			diag.warn(wn)
		}
		w.Emit = f.emit
	}

	res, err := w.Run(ctx)
	interrupted := errors.Is(err, context.Canceled)
	if err != nil && !interrupted {
		return reportFailure(err, o.format, stdout, diag)
	}
	wait := &waitOut{Satisfied: res.Satisfied, Reason: res.Reason, ElapsedMS: res.Elapsed.Milliseconds(), Transport: res.Transport}
	if f != nil {
		f.finish(diag, wait)
	} else {
		writeResult(stdout, diag, o.format, result{
			scope:    so,
			entries:  res.Entries,
			warnings: append(o.warnings, readWarnings(o, scope, res.Entries, res.WindowMinutes)...),
			wait:     wait,
		})
	}
	return waitExit(interrupted, o.waiting() && !res.Satisfied, silent, diag)
}

// follower streams entries as a --follow wait accepts them.
type follower struct {
	opts      readOpts
	scope     *telemetryScope
	out       io.Writer
	truncated []truncatedRef
}

func (f *follower) emit(e telemetryread.Entry) {
	e.FillRef(f.scope.project.Slug)
	if telemetryread.Truncate(&e, f.opts.maxPayload) {
		f.truncated = append(f.truncated, truncatedRef{ID: e.ID, Command: getCommand(e, f.scope)})
	}
	writeEntries(f.out, f.opts.format, []telemetryread.Entry{e})
}

// finish reports what a stream can only know at its end: the entries it cut
// and, with a stop condition, how the wait ended.
func (f *follower) finish(diag diagnostics, wait *waitOut) {
	if len(f.truncated) > 0 {
		diag.warn(truncatedWarning(f.truncated, f.opts.maxPayload))
	}
	if f.opts.waiting() {
		diag.wait(*wait)
	}
}

// waitExit is a finished wait's exit: 130 when interrupted, 3 when a stop
// condition wasn't met in time (with a hint on stderr), otherwise success. A
// timeout on a silent controller says so, since waiting longer won't help.
func waitExit(interrupted, timedOut bool, silent *warning, diag diagnostics) error {
	switch {
	case interrupted:
		return silentExit(exitInterrupted)
	case timedOut:
		hint := "Trigger the driver action again, or re-run with a longer --timeout (max 10m)."
		if silent != nil {
			hint = "The controller isn't reporting: check it and its Anvil agent are running before waiting again."
		}
		if diag.json {
			e := newCmdErr("wait_timeout", "The stop condition wasn't met before --timeout.", hint)
			if silent != nil {
				e = e.with("device_not_reporting", true).with("last_seen_at", silent.Details["last_seen_at"])
			}
			diag.err(e)
		} else {
			fmt.Fprintln(diag.w, "tip: "+strings.ToLower(hint[:1])+hint[1:])
		}
		return silentExit(exitWaitTimeout)
	}
	return nil
}
