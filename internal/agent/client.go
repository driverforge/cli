// Package agent is a client for the Anvil Agent's deploy server, which runs on a
// Control4 controller (default :9443). It implements the deploy/sync/driver-state
// protocol agreed with the agent team: full publish (`/deploy`), fast Lua
// hot-swap (`/sync`), the running-driver probe (`/driver-state`), and capability
// discovery (`/healthz`). Transport is vanilla net/http; auth is the user's
// OAuth bearer token (from internal/auth).
package agent

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/driverforge/cli/internal/auth"
	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/sanitize"
)

// Machine error codes returned by the agent (handover §7).
const (
	CodeNeedsDeploy       = "needs_deploy"
	CodeNotInstalled      = "not_installed"
	CodeSelfSyncForbidden = "self_sync_forbidden"
	// CodeNotInProject is returned by /deploy when the driver isn't yet part of
	// the director's project. It's the trigger for the first-deploy bootstrap:
	// the CLI offers to install the driver into a chosen location rather than
	// failing. Branch on this code, never the message.
	CodeNotInProject = "not_in_project"
)

// ErrFingerprintMismatch marks a refused connection because the controller
// presented a TLS certificate that doesn't match the pinned fingerprint (e.g.
// the agent was reinstalled with a fresh self-signed cert). It's wrapped inside
// the friendly UserError pinnedFingerprint returns, so callers can errors.Is it
// to self-heal (re-fetch the fingerprint) before surfacing the error.
var ErrFingerprintMismatch = errors.New("agent: controller certificate fingerprint mismatch")

// Error is a structured agent error carrying the machine code so callers can
// branch (e.g. needs_deploy → escalate to a full deploy).
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Code != "" {
		return e.Code
	}
	return fmt.Sprintf("agent: HTTP %d", e.Status)
}

// Code returns the machine error code of err if it is an agent Error.
func Code(err error) string {
	var ae *Error
	if errors.As(err, &ae) {
		return ae.Code
	}
	return ""
}

// Client talks to one controller's deploy server.
type Client struct {
	baseURL string
	http    *http.Client
}

// New returns a client for baseURL (e.g. https://192.168.1.10:9443). The
// controller serves a self-signed cert on a DHCP IP, so we can't verify a
// CA/hostname; instead we pin the leaf certificate's SHA-256 fingerprint. When
// fingerprint is empty, DRIVERFORGE_AGENT_INSECURE=1 skips verification entirely (dev
// only); otherwise the default transport applies (callers refuse before here).
func New(baseURL, fingerprint string) *Client {
	tr := &http.Transport{}
	switch {
	case fingerprint != "":
		tr.TLSClientConfig = &tls.Config{
			InsecureSkipVerify:    true, // we pin instead of CA/hostname verification
			VerifyPeerCertificate: pinnedFingerprint(fingerprint),
		}
	case os.Getenv("DRIVERFORGE_AGENT_INSECURE") != "":
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 30 * time.Second, Transport: tr},
	}
}

// pinnedFingerprint verifies the server's leaf certificate's SHA-256 fingerprint
// matches expected. It runs even with InsecureSkipVerify, which is the standard
// certificate-pinning pattern.
func pinnedFingerprint(expected string) func([][]byte, [][]*x509.Certificate) error {
	want := normalizeFingerprint(expected)
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return fmt.Errorf("agent: server presented no certificate")
		}
		sum := sha256.Sum256(rawCerts[0]) // leaf, DER-encoded
		if !strings.EqualFold(hex.EncodeToString(sum[:]), want) {
			// Anticipated, user-facing condition (the controller's cert rotated):
			// a friendly UserError so it isn't reported as a crash. It survives
			// net/http + TLS wrapping via errors.As at the top level.
			return &clierr.UserError{
				Title:   "Controller certificate changed",
				Message: "the controller's TLS certificate doesn't match the pinned fingerprint — refusing to connect",
				Hint:    "re-run `driverforge device select` if the controller's certificate changed",
				Err:     ErrFingerprintMismatch,
			}
		}
		return nil
	}
}

// normalizeFingerprint reduces a fingerprint to lowercase hex with no
// separators, accepting a "sha256:" prefix, colon-separated hex, or base64.
func normalizeFingerprint(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, ':'); i >= 3 {
		if p := strings.ToLower(s[:i]); p == "sha256" || p == "sha-256" {
			s = s[i+1:]
		}
	}
	hexForm := strings.ToLower(strings.NewReplacer(":", "", " ", "").Replace(s))
	if len(hexForm) == 64 && isHex(hexForm) {
		return hexForm
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(strings.TrimSpace(s)); err == nil && len(b) == 32 {
			return hex.EncodeToString(b)
		}
	}
	return hexForm
}

func isHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Health reports the agent's version and capabilities (no auth).
type Health struct {
	AgentVersion  string   `json:"agentVersion"`
	AgentFilename string   `json:"agentFilename"` // exact installed .c4z name (e.g. anvil-agent-dev.c4z); enables self-upgrade
	Protocol      int      `json:"protocol"`
	Capabilities  []string `json:"capabilities"`
	// Controller identity/version (additive in protocol 4; empty on older
	// agents). ControllerVersion is the Director/OS version.
	ControllerVersion string `json:"controllerVersion"`
	ControllerName    string `json:"controllerName"`
	ControllerMac     string `json:"controllerMac"`
}

// Supports reports whether the agent advertises a capability (e.g. "sync").
func (h Health) Supports(cap string) bool {
	for _, c := range h.Capabilities {
		if c == cap {
			return true
		}
	}
	return false
}

// Health probes /healthz, which needs no authentication — so it doubles as the
// reachability check every command runs before doing real work. Callers gate
// optional behaviour on the returned Capabilities rather than on AgentVersion, so
// an agent too old for a feature degrades rather than fails.
func (c *Client) Health(ctx context.Context) (Health, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/healthz", nil)
	if err != nil {
		return Health{}, err
	}
	var h Health
	if err := c.do(req, &h); err != nil {
		return Health{}, err
	}
	h.AgentVersion = sanitize.Terminal(h.AgentVersion)
	h.AgentFilename = sanitize.Terminal(h.AgentFilename)
	h.ControllerVersion = sanitize.Terminal(h.ControllerVersion)
	h.ControllerName = sanitize.Terminal(h.ControllerName)
	h.ControllerMac = sanitize.Terminal(h.ControllerMac)
	return h, nil
}

// DriverState is the agent's view of an installed driver (the cheap "what's
// running" probe). StructuralHash is "" when the agent has never recorded one.
type DriverState struct {
	OK             bool   `json:"ok"`
	Installed      bool   `json:"installed"`
	DeviceCount    int    `json:"deviceCount"`
	StructuralHash string `json:"structuralHash"`
	AgentVersion   string `json:"agentVersion"`
}

// DriverState asks the controller what it currently has for filename: whether the
// driver is installed, how many devices use it, and the structural hash the agent
// last recorded. Sync uses the hash to decide whether a Lua hot-swap is safe or
// whether the change needs a full deploy.
func (c *Client) DriverState(ctx context.Context, filename string) (DriverState, error) {
	u := c.baseURL + "/driver-state?driver=" + url.QueryEscape(filename)
	req, err := c.authed(ctx, http.MethodGet, u, "", nil)
	if err != nil {
		return DriverState{}, err
	}
	var s DriverState
	if err := c.do(req, &s); err != nil {
		return DriverState{}, err
	}
	return s, nil
}

// RefreshNavigators asks the agent to trigger a Refresh Navigators on the
// controller, so navigators re-read cached driver UI schemas. How the agent
// effects that is its own concern. Only call when the agent advertises
// CapabilityNavigatorRefresh.
func (c *Client) RefreshNavigators(ctx context.Context) error {
	req, err := c.authed(ctx, http.MethodPost, c.baseURL+"/refresh-navigators", "", nil)
	if err != nil {
		return err
	}
	return c.do(req, nil)
}

// DeployResult echoes the stored structural hash so the CLI can cache it.
type DeployResult struct {
	OK             bool   `json:"ok"`
	Filename       string `json:"filename"`
	Bytes          int    `json:"bytes"`
	Reinstall      string `json:"reinstall"`
	StructuralHash string `json:"structuralHash"`
}

// DeployStream uploads the full .c4z and, when optIn is set (the agent advertises
// the deploy-stream capability), reads the agent's NDJSON progress stream — one
// DeployEvent per line — invoking onEvent (may be nil) for each as it arrives,
// until the first event with Terminal true. It returns the final DeployResult
// and that terminal event.
//
// A pre-streaming Driverforge Agent (or optIn false) returns a single DeployResult
// object with no "state" field; DeployStream detects that and synthesizes a
// staged→confirmed pair so the progress UI still animates. The stream has no
// per-call timeout — a real restart can outlast the 30s default — so it is
// bounded only by ctx; a close without a terminal event surfaces as interrupted.
func (c *Client) DeployStream(ctx context.Context, filename, structuralHash, sha256 string, c4z io.Reader, optIn bool, onEvent func(DeployEvent)) (DeployResult, DeployEvent, error) {
	req, err := c.authed(ctx, http.MethodPost, c.baseURL+"/deploy", filename, c4z)
	if err != nil {
		return DeployResult{}, DeployEvent{}, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	if optIn {
		req.Header.Set("X-Deploy-Stream", "1")
		req.Header.Set("Accept", "application/x-ndjson")
	}
	if structuralHash != "" {
		req.Header.Set("X-Driver-Structural-Hash", structuralHash)
	}
	// Integrity for bytes pulled over the network (agent upgrade): the agent
	// verifies and rejects checksum_mismatch. Empty for a local build deploy.
	if sha256 != "" {
		req.Header.Set("X-Driver-SHA256", sha256)
	}

	resp, err := c.streamingHTTP().Do(req)
	if err != nil {
		return DeployResult{}, DeployEvent{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return DeployResult{}, DeployEvent{}, parseErrorEnvelope(resp.StatusCode, data)
	}
	return readDeployStream(resp.Body, filename, onEvent)
}

// readDeployStream consumes the agent's NDJSON deploy/install progress stream
// from body — one DeployEvent per object — invoking onEvent (may be nil) for each
// until the first Terminal event, then returning the final DeployResult and that
// event. It is shared by /deploy and /install, which stream identical progress.
//
// A pre-streaming Driverforge Agent returns a single DeployResult object with no
// "state" field; that is detected and synthesized into a staged→confirmed pair so
// the progress UI still animates. A close without a terminal event surfaces as an
// interrupted stream.
func readDeployStream(body io.Reader, filename string, onEvent func(DeployEvent)) (DeployResult, DeployEvent, error) {
	emit := func(e DeployEvent) {
		if onEvent != nil {
			onEvent(e)
		}
	}

	// json.Decoder reads NDJSON (and chunked/partial lines) incrementally.
	dec := json.NewDecoder(body)
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				return DeployResult{}, DeployEvent{}, fmt.Errorf("agent: deploy stream ended before completion")
			}
			return DeployResult{}, DeployEvent{}, fmt.Errorf("agent: decoding deploy event: %w", err)
		}

		// A non-empty "state" marks a streamed event; its absence marks a pre-streaming
		// one-shot DeployResult object.
		var probe struct {
			State DeployState `json:"state"`
		}
		_ = json.Unmarshal(raw, &probe)
		if probe.State == "" {
			// Legacy one-shot response. An explicit {"ok":false} is a failure,
			// not a deploy — synthesizing "confirmed" from it would report a
			// success the agent never claimed (e.g. an error body a proxy
			// returned with a 200).
			if failed, aerr := envelopeFailed(200, raw); failed {
				return DeployResult{}, DeployEvent{}, aerr
			}
			var r DeployResult
			if err := json.Unmarshal(raw, &r); err != nil {
				return DeployResult{}, DeployEvent{}, fmt.Errorf("agent: decoding deploy response: %w", err)
			}
			emit(DeployEvent{State: StateStaged, Step: "driver written to the controller"})
			done := DeployEvent{State: StateConfirmed, Step: "driver reloaded on the controller", Terminal: true}
			emit(done)
			return r, done, nil
		}

		var ev DeployEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			return DeployResult{}, DeployEvent{}, fmt.Errorf("agent: decoding deploy event: %w", err)
		}
		ev.scrub() // remote display strings: strip terminal control sequences
		emit(ev)
		if ev.Terminal {
			return DeployResult{OK: ev.State != StateFailed, Filename: filename}, ev, nil
		}
	}
}

// streamingHTTP shares the pinned transport but drops the 30s overall timeout, so
// a streamed deploy is bounded only by the request context.
func (c *Client) streamingHTTP() *http.Client {
	return &http.Client{Transport: c.http.Transport}
}

// SyncResult reports the per-device outcome of a hot-swap.
type SyncResult struct {
	OK             bool     `json:"ok"`
	Synced         int      `json:"synced"`
	Devices        []string `json:"devices"`
	StructuralHash string   `json:"structuralHash"`
	Verified       bool     `json:"verified"`
}

// UnmarshalJSON decodes a sync envelope, tolerating device identifiers sent as
// JSON numbers (Control4 device IDs are integers) as well as strings. The field
// is display-only ("synced 2 device(s): 12, 34"), so an unexpected shape must
// never fail the whole sync — it degrades to the raw token instead.
func (r *SyncResult) UnmarshalJSON(b []byte) error {
	type alias SyncResult // avoid recursing into this method
	aux := struct {
		Devices []json.RawMessage `json:"devices"`
		*alias
	}{alias: (*alias)(r)}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	r.Devices = nil
	for _, raw := range aux.Devices {
		r.Devices = append(r.Devices, jsonScalarString(raw))
	}
	return nil
}

// jsonScalarString renders a JSON string or number as a plain string, falling
// back to the raw token for any other shape.
func jsonScalarString(raw []byte) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err == nil {
		return n.String()
	}
	return strings.TrimSpace(string(raw))
}

// Sync hot-swaps the squished driver.lua into the running driver(s). deviceID is
// optional (empty = all instances). A structural mismatch returns an Error with
// Code == CodeNeedsDeploy.
func (c *Client) Sync(ctx context.Context, filename, structuralHash, deviceID string, lua io.Reader) (SyncResult, error) {
	req, err := c.authed(ctx, http.MethodPost, c.baseURL+"/sync", filename, lua)
	if err != nil {
		return SyncResult{}, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Driver-Structural-Hash", structuralHash)
	if deviceID != "" {
		req.Header.Set("X-Driver-Device", deviceID)
	}
	var r SyncResult
	if err := c.do(req, &r); err != nil {
		return SyncResult{}, err
	}
	for i, d := range r.Devices { // device names are rendered to the terminal
		r.Devices[i] = sanitize.Terminal(d)
	}
	return r, nil
}

// authed builds a request with an agent-scoped bearer token (narrowed to the
// deploy-server audience so it can't be replayed against graph) and the
// X-Driver-Filename header.
func (c *Client) authed(ctx context.Context, method, u, filename string, body io.Reader) (*http.Request, error) {
	token, err := auth.AgentAccessToken(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if filename != "" {
		req.Header.Set("X-Driver-Filename", filename)
	}
	return req, nil
}

// do executes req and decodes a {ok,...} JSON envelope into out, mapping
// non-2xx / {ok:false} responses to *Error.
func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode/100 != 2 {
		return parseErrorEnvelope(resp.StatusCode, data)
	}
	// A 2xx with {"ok":false} is still a failure — the agent reports the
	// outcome in the body, not only the status line. Without this a
	// {"ok":false,"error":…} 200 would decode silently (e.g. Sync printing
	// "✓ synced 0 device(s)" for a rejected hot-swap).
	if failed, aerr := envelopeFailed(resp.StatusCode, data); failed {
		return aerr
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("agent: decoding response: %w", err)
		}
	}
	return nil
}

// envelopeFailed reports whether a 2xx response body explicitly declares
// failure via {"ok":false}, returning the matching *Error. A body with no "ok"
// field (or ok:true) is not treated as a failure — not every endpoint carries
// the flag.
func envelopeFailed(status int, data []byte) (bool, error) {
	var env struct {
		OK *bool `json:"ok"`
	}
	if json.Unmarshal(data, &env) != nil || env.OK == nil || *env.OK {
		return false, nil
	}
	return true, parseErrorEnvelope(status, data)
}

// parseErrorEnvelope maps a non-2xx response body to a structured *Error,
// pulling the agent's {error,message} envelope when present.
func parseErrorEnvelope(status int, data []byte) *Error {
	ae := &Error{Status: status}
	var env struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &env) == nil {
		ae.Code, ae.Message = sanitize.Terminal(env.Error), sanitize.Terminal(env.Message)
	}
	return ae
}
