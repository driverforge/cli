package metrics

import (
	"context"
	"os"
	"sync"
	"time"
)

// flushTimeout bounds the total time spent delivering telemetry at the end of a
// command, mirroring updatecheck's deliberately short network budget.
const flushTimeout = 3 * time.Second

// Client buffers telemetry during a command and fans it out to the configured
// sinks on Flush. It is safe to use as a nil pointer (all methods no-op), so
// callers never need a nil guard. When metrics are disabled (no consent or an
// opt-out env var), Track is a no-op.
type Client struct {
	enabled bool
	id      Identity
	sinks   []Sink

	mu     sync.Mutex
	events []BusinessEvent
}

// New builds a Client from the loaded config. Metrics are enabled only when the
// user has granted consent and no opt-out env var is set.
func New(cfg Config) *Client {
	return &Client{
		enabled: cfg.MetricsConsent == ConsentGranted && !optedOut(),
		id:      Identity{DeviceID: cfg.AnonymousID},
		sinks:   defaultSinks(),
	}
}

// SetUser attaches the signed-in account's subject id, uniting this install's
// events with the user's server-side analytics person. A no-op on a nil/
// disabled client or an empty subject (signed-out runs stay device-only).
func (c *Client) SetUser(sub string) {
	if !c.Enabled() || sub == "" {
		return
	}
	c.mu.Lock()
	c.id.UserID = sub
	c.mu.Unlock()
}

// SetOrg attaches the active organization id, dimensioning this run's events
// for org-level analysis. A no-op on a nil/disabled client or an empty id.
func (c *Client) SetOrg(orgID string) {
	if !c.Enabled() || orgID == "" {
		return
	}
	c.mu.Lock()
	c.id.OrgID = orgID
	c.mu.Unlock()
}

// Enabled reports whether telemetry will actually be collected/sent.
func (c *Client) Enabled() bool { return c != nil && c.enabled }

// Track records a business event (no-op when disabled).
func (c *Client) Track(e BusinessEvent) {
	if !c.Enabled() {
		return
	}
	c.mu.Lock()
	c.events = append(c.events, e)
	c.mu.Unlock()
}

// Flush delivers all buffered telemetry to the sinks concurrently, bounded by
// flushTimeout. All errors are swallowed; Flush never affects the caller.
func (c *Client) Flush(ctx context.Context) {
	if !c.Enabled() {
		return
	}
	c.mu.Lock()
	events, id := c.events, c.id
	c.events = nil
	c.mu.Unlock()
	if len(events) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, flushTimeout)
	defer cancel()

	var wg sync.WaitGroup
	for _, s := range c.sinks {
		wg.Add(1)
		go func(s Sink) {
			defer wg.Done()
			_ = s.Send(ctx, id, events) // best-effort
		}(s)
	}
	wg.Wait()
}

// optedOut reports whether telemetry is disabled by environment. CI is treated
// as opted-out so automated runs never emit (and are never prompted).
func optedOut() bool {
	return os.Getenv("DRIVERFORGE_NO_METRICS") != "" ||
		os.Getenv("DO_NOT_TRACK") != "" ||
		os.Getenv("CI") != ""
}
