// Package telemetry connects the CLI to the same real-time channel the Anvil web
// app uses to receive live driver telemetry (Centrifugo). Its purpose today is
// narrow: after a first-time install, wait for the driver's first reported event
// so the CLI can confirm the driver is not just running on the controller but
// reporting back to Driverforge, and deep-link the installer to it.
//
// Everything here is best-effort: a driver deploy must never depend on telemetry
// being reachable, so callers treat any setup error as "skip the celebration".
package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"

	centrifuge "github.com/centrifugal/centrifuge-go"
)

// debugEnabled reports whether telemetry diagnostics should be printed to stderr.
// Off by default; this is a best-effort feature and must stay silent normally.
func debugEnabled() bool { return os.Getenv("DRIVERFORGE_DEBUG") != "" }

func debugf(format string, args ...any) {
	if debugEnabled() {
		fmt.Fprintf(os.Stderr, "[telemetry] "+format+"\n", args...)
	}
}

// Event is the subset of a telemetry publication the CLI cares about. The web
// app reads the same shape off each Centrifugo publication (a "type" discriminator
// plus the event fields). Unknown fields are ignored.
type Event struct {
	Type       string            `json:"type"` // "event" | "log" | "error"
	Name       string            `json:"name"` // e.g. "OnDriverInit"
	TraceID    string            `json:"traceId"`
	Driver     string            `json:"driver"`
	Controller string            `json:"controller"`
	Tags       map[string]string `json:"tags"`
}

// IsDriverEvent reports whether a publication is a driver-fired event (as opposed
// to a log or heartbeat). Any event proves the freshly-installed driver is alive
// and reporting to Anvil — which is exactly what the first-deploy celebration
// confirms.
//
// We deliberately don't key on a specific method name. OnDriverInit fires while
// the telemetry pipeline is still coming up and often isn't captured at all;
// OnDriverLateInit, OnPropertyChanged, ReceivedFromProxy, etc. reliably are. Since
// a project maps 1:1 to a driver, every event on the channel is this driver's, so
// the first event is a sound "it checked in" signal. (Narrowing to a specific
// controller, for one driver on several controllers, is still deferred.)
func IsDriverEvent(e Event) bool {
	return e.Type == "event"
}

// TokenFunc mints a Centrifugo connection token. driverforgeapi.Client.CentrifugoToken
// satisfies it directly.
type TokenFunc func(context.Context) (string, error)

// Watcher holds a live subscription to a project's telemetry channel and streams
// the publications matching a predicate.
type Watcher struct {
	client *centrifuge.Client
	sub    *centrifuge.Subscription
	events chan Event
}

// channel is the Centrifugo channel a project publishes telemetry on.
func channel(projectID string) string { return "project:" + projectID }

// NewWatcher connects to Centrifugo and subscribes to the project's channel,
// buffering the first publication for which match returns true so a caller that
// starts waiting only after the deploy confirms doesn't miss an event that
// arrived in between. The connection token is fetched up front so an
// unauthenticated/offline caller fails fast (and skips the feature) rather than
// spinning up a doomed connection. Call Close when done.
func NewWatcher(ctx context.Context, projectID string, getToken TokenFunc, match func(Event) bool) (*Watcher, error) {
	token, err := getToken(ctx)
	if err != nil {
		return nil, err
	}

	client := newClient(centrifuge.Config{Token: token})

	sub, err := client.NewSubscription(channel(projectID))
	if err != nil {
		client.Close()
		return nil, err
	}

	w := &Watcher{client: client, sub: sub, events: make(chan Event, 256)}

	// Diagnostics: without these, a rejected handshake or subscribe fails silently
	// and the caller just hangs until timeout. All no-ops unless DRIVERFORGE_DEBUG.
	ch := channel(projectID)
	client.OnConnected(func(centrifuge.ConnectedEvent) { debugf("connected") })
	client.OnError(func(e centrifuge.ErrorEvent) { debugf("client error: %v", e.Error) })
	client.OnDisconnected(func(e centrifuge.DisconnectedEvent) { debugf("disconnected: code=%d reason=%q", e.Code, e.Reason) })
	sub.OnSubscribed(func(centrifuge.SubscribedEvent) { debugf("subscribed to %s", ch) })
	sub.OnError(func(e centrifuge.SubscriptionErrorEvent) { debugf("subscribe error on %s: %v", ch, e.Error) })
	sub.OnUnsubscribed(func(e centrifuge.UnsubscribedEvent) {
		debugf("unsubscribed from %s: code=%d reason=%q", ch, e.Code, e.Reason)
	})

	sub.OnPublication(func(e centrifuge.PublicationEvent) {
		var ev Event
		if json.Unmarshal(e.Data, &ev) != nil {
			debugf("publication (unparseable): %s", clip(e.Data))
			return
		}
		debugf("publication: type=%q name=%q traceId=%q match=%v", ev.Type, ev.Name, ev.TraceID, match(ev))
		if !match(ev) {
			return
		}
		// Stream matches, non-blocking so a flood never stalls the SDK's callback
		// goroutine. A full buffer just drops — the consumer counts events as a
		// liveness signal, not an audit.
		select {
		case w.events <- ev:
		default:
		}
	})

	if err := sub.Subscribe(); err != nil {
		client.Close()
		return nil, err
	}
	if err := client.Connect(); err != nil {
		client.Close()
		return nil, err
	}
	return w, nil
}

// newClient builds a Centrifugo client for the telemetry endpoint from cfg,
// adding what every CLI connection needs: its name, the web app's Origin, and
// the SDK's debug log when DRIVERFORGE_DEBUG is set.
func newClient(cfg centrifuge.Config) *centrifuge.Client {
	cfg.Name = "driverforge-cli"
	// Centrifugo refuses WebSocket handshakes whose Origin isn't in allowed_origins
	// (a browser CSRF guard). A headless client sends no Origin, so present the web
	// app's — the same one browsers use — or the connection is silently rejected.
	if origin := resolveCentrifugoOrigin(); origin != "" {
		cfg.Header = http.Header{"Origin": []string{origin}}
	}
	if debugEnabled() {
		cfg.LogLevel = centrifuge.LogLevelDebug
		cfg.LogHandler = func(e centrifuge.LogEntry) { debugf("sdk[%s] %s %v", e.Level, e.Message, e.Fields) }
	}
	return centrifuge.NewJsonClient(resolveCentrifugoURL(), cfg)
}

// clip bounds a byte slice for a debug line.
func clip(b []byte) string {
	const max = 200
	if len(b) > max {
		return string(b[:max]) + "…"
	}
	return string(b)
}

// Events streams the matching publications as they arrive. It's buffered and
// lossy under a flood (see NewWatcher). The channel is never closed; stop
// reading when your own context is done. Returns nil on a nil watcher.
func (w *Watcher) Events() <-chan Event {
	if w == nil {
		return nil
	}
	return w.events
}

// Close tears down the subscription and connection. Safe on a nil watcher and
// idempotent enough for a deferred call.
func (w *Watcher) Close() {
	if w == nil || w.client == nil {
		return
	}
	w.client.Close()
}

// EventsURL is the Anvil web link to a project's event stream — the org's events
// index filtered to the project, not a single event. A specific event's UUID is
// ugly in a terminal and a randomly-picked one is orphaned from anything
// actionable; the index is where the installer actually wants to land.
//
// Release builds route through the shortener so the events page can be re-routed
// later without stranding binaries already in the field: both slugs ride in the
// query string, which the short link forwards to the app's /a handler, and that
// swaps the org into the path. This is indirection for reroutability, not brevity
// — the short form is marginally longer than the direct URL.
//
// Builds with no short-link base (local, dev, forks — see ShortLinkURL) emit the
// direct URL instead: the same destination the /a handler would have redirected
// to, so a dev stack needs no /a route to make the link work.
//
// Published contract: released CLI versions print the short form and keep printing
// it for as long as they stay installed. The /events slug and its org/project
// parameters can never be retired — only repointed.
func EventsURL(orgSlug, projectSlug string) string {
	if base := resolveShortLinkURL(); base != "" {
		q := url.Values{"org": {orgSlug}, "project": {projectSlug}}
		return base + "/events?" + q.Encode()
	}
	return resolveAppURL() + "/" + orgSlug + "/events/?project=" + url.QueryEscape(projectSlug)
}
