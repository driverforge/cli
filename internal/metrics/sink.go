package metrics

import (
	"context"
	"io"
	"net/http"
)

// httpClient is the shared client for the telemetry sinks. Every Send already
// runs under the flushTimeout context, but a client-level timeout matches the
// convention used across the other packages (auth, driverforgeapi, updatecheck…) and
// fails closed if a sink is ever called with a context that carries no deadline.
var httpClient = &http.Client{Timeout: flushTimeout}

// Identity carries the IDs a batch of events is attributed to. DeviceID is the
// CLI's per-install anonymous id (always set). UserID is the Driverforge
// account subject when the user is signed in ("" otherwise) — with both set,
// Mixpanel's ID merge unites the install's pre-login history with the account.
// OrgID is the active organization ("" when none selected); it rides on events
// as the `organization` property, matching the platform analytics library's
// group key so org-level views span web and CLI.
type Identity struct {
	DeviceID string
	UserID   string
	OrgID    string
}

// Sink delivers a batch of buffered telemetry to a backend. A Sink that has no
// endpoint/token configured is "inert" and returns nil without sending, so the
// feature ships safely dark until release tokens are wired in.
type Sink interface {
	// Name identifies the sink (for diagnostics only).
	Name() string
	// Send delivers the events. It must respect ctx's deadline and should
	// return any transport error (the caller swallows it).
	Send(ctx context.Context, id Identity, events []BusinessEvent) error
}

// drainClose drains and closes an HTTP response body so the connection can be
// reused.
func drainClose(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	_ = resp.Body.Close()
}
