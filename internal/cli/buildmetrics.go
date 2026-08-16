package cli

import (
	"time"

	"github.com/driverforge/cli/internal/buildinfo"
	"github.com/driverforge/cli/internal/metrics"
	"github.com/driverforge/cli/pkg/c4z"
)

// stepRecorder observes a build's progress events to capture per-step wall-clock
// durations, which the run frame renders as "(12ms)" suffixes on its step log
// lines. It lives in the CLI layer so pkg/c4z stays timing-free.
type stepRecorder struct {
	starts map[c4z.Step]time.Time
	durs   map[c4z.Step]time.Duration
	order  []c4z.Step
}

func newStepRecorder() *stepRecorder {
	return &stepRecorder{starts: map[c4z.Step]time.Time{}, durs: map[c4z.Step]time.Duration{}}
}

func (r *stepRecorder) observe(e c4z.Event) {
	switch e.Status {
	case c4z.StepStart:
		r.starts[e.Step] = time.Now()
	case c4z.StepOK, c4z.StepFail, c4z.StepSkip:
		if t, ok := r.starts[e.Step]; ok {
			if _, seen := r.durs[e.Step]; !seen {
				r.order = append(r.order, e.Step)
			}
			r.durs[e.Step] = time.Since(t)
		}
	}
}

// wrapProgress returns a progress callback that records step timings and then
// forwards to inner (which may be nil).
func (r *stepRecorder) wrapProgress(inner func(c4z.Event)) func(c4z.Event) {
	return func(e c4z.Event) {
		r.observe(e)
		if inner != nil {
			inner(e)
		}
	}
}

// emitBuildMetrics records the business event for a finished build. It is a
// no-op when metricsClient is nil or disabled.
func emitBuildMetrics(env string, res c4z.Result, buildErr error, total time.Duration) {
	envLabel := env
	if envLabel == "" {
		envLabel = "default"
	}

	metricsClient.Track(metrics.BusinessEvent{
		Name: "build",
		Properties: map[string]any{
			"command":            "build",
			"env":                envLabel,
			"success":            buildErr == nil,
			"driverType":         "c4z",
			"encrypted":          res.Encrypted,
			"driverforgeVersion": buildinfo.Version,
			"durationMs":         total.Milliseconds(),
		},
	})
}
