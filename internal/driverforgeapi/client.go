package driverforgeapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/driverforge/cli/internal/auth"
	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/sanitize"
)

// GraphError is a GraphQL-level error returned in the response `errors` array.
// Code carries the server's extensions.code (e.g. "ENTITLEMENT_REQUIRED") when
// present, so callers can react to specific conditions; Error() preserves the
// human message behind a "graph api:" prefix.
type GraphError struct {
	Message string
	Code    string
}

func (e *GraphError) Error() string { return "graph api: " + e.Message }

// RateLimitError is an HTTP 429 from the graph. RetryAfter is the server's
// requested wait, zero when it named none.
type RateLimitError struct {
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("graph api: rate limited (retry after %s)", e.RetryAfter)
}

// parseRetryAfter reads a Retry-After header: delay-seconds or an HTTP date.
// Anything unparseable, or a date in the past, is zero.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil && t.After(now) {
		return t.Sub(now)
	}
	return 0
}

// Client is an authenticated Graph API client.
type Client struct {
	endpoint string
	http     *http.Client
}

// New returns a client targeting the configured Graph API endpoint.
func New() *Client {
	return &Client{endpoint: resolveGraphURL(), http: &http.Client{Timeout: 30 * time.Second}}
}

// Organization is a Driverforge organization the user belongs to.
type Organization struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
	Role string `json:"myRole"`
}

// defaultResponseLimit bounds a graph response body. Telemetry reads carry
// whole driver payloads and use telemetryResponseLimit instead.
const defaultResponseLimit = 1 << 20

// do executes a GraphQL operation with the current user's bearer token,
// decoding the `data` field into out. It surfaces auth.ErrNotLoggedIn (via
// auth.Current), HTTP errors, and GraphQL errors.
func (c *Client) do(ctx context.Context, query string, vars map[string]any, out any) error {
	return c.doLimit(ctx, query, vars, out, defaultResponseLimit)
}

// doLimit is do with an explicit bound on the response body, in bytes.
func (c *Client) doLimit(ctx context.Context, query string, vars map[string]any, out any, limit int64) error {
	creds, err := auth.Current(ctx)
	if err != nil {
		return err
	}
	reqBody, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+creds.AccessToken)

	resp, err := c.http.Do(req)
	if err != nil {
		// Transport failure (offline, DNS, TLS, timeout): anticipated, not a bug.
		return &clierr.UserError{
			Code:    clierr.CodeConnectionLost,
			Title:   "Can't reach Driverforge",
			Message: "the request to Driverforge couldn't be completed",
			Hint:    "check your connection and try again",
			Err:     err,
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return &clierr.UserError{
			Code:    clierr.CodeAuthRequired,
			Title:   "Session expired",
			Message: "your session may have expired",
			Hint:    "run `driverforge login` and try again",
			Err:     auth.ErrSessionExpired,
		}
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return &RateLimitError{RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())}
	}
	if resp.StatusCode >= 500 {
		// A gateway/backend hiccup (502/503/…): transient, retryable, not a CLI bug.
		return &clierr.UserError{
			Code:    clierr.CodeServiceUnavailable,
			Title:   "Driverforge is unavailable",
			Message: "Driverforge returned a temporary error",
			Hint:    "try again in a moment",
			Err:     fmt.Errorf("graph api: HTTP %d", resp.StatusCode),
		}
	}
	if resp.StatusCode/100 != 2 {
		// The body is server-controlled and this error can reach the terminal
		// (and Sentry) — strip any terminal control sequences.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<14))
		return fmt.Errorf("graph api: HTTP %d: %s", resp.StatusCode, sanitize.Terminal(strings.TrimSpace(string(body))))
	}

	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message    string `json:"message"`
			Extensions struct {
				Code string `json:"code"`
			} `json:"extensions"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, limit)).Decode(&envelope); err != nil {
		return fmt.Errorf("graph api: decoding response: %w", err)
	}
	if len(envelope.Errors) > 0 {
		return &GraphError{
			Message: envelope.Errors[0].Message,
			Code:    envelope.Errors[0].Extensions.Code,
		}
	}
	if out != nil {
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return fmt.Errorf("graph api: decoding data: %w", err)
		}
	}
	return nil
}

const myOrganizationsQuery = `query { myOrganizations { id name slug myRole } }`

// ListOrganizations returns the organizations the authenticated user belongs to.
func (c *Client) ListOrganizations(ctx context.Context) ([]Organization, error) {
	var data struct {
		MyOrganizations []Organization `json:"myOrganizations"`
	}
	if err := c.do(ctx, myOrganizationsQuery, nil, &data); err != nil {
		return nil, err
	}
	return data.MyOrganizations, nil
}

// Device is a controller registered to an organization (a Control4 controller
// running the Anvil Agent).
type Device struct {
	ID           string `json:"id"`
	FriendlyName string `json:"friendlyName"`
	Hostname     string `json:"hostname"`
	IP           string `json:"ip"`
	DeviceID     string `json:"deviceId"`
	Platform     string `json:"platform"`
	Status       string `json:"status"`
}

// DisplayName is the best human label for a device.
func (d Device) DisplayName() string {
	switch {
	case d.FriendlyName != "":
		return d.FriendlyName
	case d.Hostname != "":
		return d.Hostname
	default:
		return d.DeviceID
	}
}

const deployableDevicesQuery = `query($orgId: String!) { deployableDevices(organizationId: $orgId) { id friendlyName hostname ip deviceId platform status } }`

// ListDevices returns the devices deployable for the given organization — the
// union of the org's devices and the caller's own (user-associated) controllers,
// so a freshly set-up controller is selectable before any driver/org association.
func (c *Client) ListDevices(ctx context.Context, organizationID string) ([]Device, error) {
	var data struct {
		Devices []Device `json:"deployableDevices"`
	}
	if err := c.do(ctx, deployableDevicesQuery, map[string]any{"orgId": organizationID}, &data); err != nil {
		return nil, err
	}
	return data.Devices, nil
}

const updateDeviceNameMutation = `mutation($organizationId: String!, $input: UpdateDeviceNameInput!) { updateDeviceName(organizationId: $organizationId, input: $input) { id friendlyName hostname ip deviceId platform status } }`

// UpdateDeviceName sets the org-scoped friendly name for a device — the same
// name the Anvil UI shows, so naming a controller here renames it everywhere.
// id is the association id (Device.ID from ListDevices), not the hardware
// deviceId. An empty name clears the override and the device falls back to its
// hostname.
func (c *Client) UpdateDeviceName(ctx context.Context, organizationID, id, name string) (Device, error) {
	var data struct {
		Device Device `json:"updateDeviceName"`
	}
	vars := map[string]any{
		"organizationId": organizationID,
		"input":          map[string]any{"deviceId": id, "friendlyName": nullableName(name)},
	}
	if err := c.do(ctx, updateDeviceNameMutation, vars, &data); err != nil {
		return Device{}, err
	}
	return data.Device, nil
}

// nullableName maps an empty name to a JSON null, which is how the mutation
// spells "clear the override" — sending "" would work today (the service
// trims and nils it) but null is the documented contract.
func nullableName(name string) any {
	if strings.TrimSpace(name) == "" {
		return nil
	}
	return name
}

const deviceFingerprintQuery = `query($deviceId: String!) { device(deviceId: $deviceId) { deviceId certFingerprint } }`

// DeviceFingerprint returns the controller's pinned TLS certificate fingerprint
// for the given hardware device id (MAC), or "" if the device exists but hasn't
// completed cert registration (certFingerprint is null).
func (c *Client) DeviceFingerprint(ctx context.Context, deviceID string) (string, error) {
	var data struct {
		Device struct {
			DeviceID        string `json:"deviceId"`
			CertFingerprint string `json:"certFingerprint"`
		} `json:"device"`
	}
	if err := c.do(ctx, deviceFingerprintQuery, map[string]any{"deviceId": deviceID}, &data); err != nil {
		return "", err
	}
	return data.Device.CertFingerprint, nil
}

// Project is a Driverforge project: the thing a driver reports telemetry under.
//
// Token identifies the project to the telemetry pipeline. It is an identifier,
// not a credential: it authorises nothing, does not expire, cannot be revoked,
// and is not secret. `driverforge init` embeds it in the driver source, where it
// is committed, packaged and shipped to controllers, all by design.
//
// Its prefix varies by project kind (drv_ for drivers, agt_ for the agent), so
// treat the prefix as display only and never as something to validate against.
type Project struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Slug           string `json:"slug"`
	Platform       string `json:"platform"`
	OrganizationID string `json:"organizationId"`
	Token          string `json:"token"`
}

// Sentinel conditions a caller may want to present specially (e.g. an upgrade
// prompt). CreateProject wraps these around the underlying GraphError.
var (
	// ErrProjectLimit means the org's plan doesn't allow creating another project.
	ErrProjectLimit = errors.New("project limit reached for this plan")
	// ErrSubscriptionSuspended means the org's subscription is suspended (payment).
	ErrSubscriptionSuspended = errors.New("subscription suspended")
)

const myProjectsQuery = `query { myProjects { id name slug platform organizationId token } }`

// ListAllProjects returns every project the authenticated user belongs to, across
// all organizations (used to resolve a bare project slug).
func (c *Client) ListAllProjects(ctx context.Context) ([]Project, error) {
	var data struct {
		MyProjects []Project `json:"myProjects"`
	}
	if err := c.do(ctx, myProjectsQuery, nil, &data); err != nil {
		return nil, err
	}
	return data.MyProjects, nil
}

// ListProjects returns the authenticated user's projects scoped to the given
// organization.
func (c *Client) ListProjects(ctx context.Context, organizationID string) ([]Project, error) {
	all, err := c.ListAllProjects(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Project, 0, len(all))
	for _, p := range all {
		if p.OrganizationID == organizationID {
			out = append(out, p)
		}
	}
	return out, nil
}

// CreateProjectInput holds the fields needed to create a project. Platform
// defaults to CONTROL4 when empty.
type CreateProjectInput struct {
	Name           string
	OrganizationID string
	Platform       string
	Slug           string
	Description    string
}

const createProjectMutation = `mutation($input: CreateProjectInput!) { createProject(input: $input) { id name slug platform organizationId token } }`

// CreateProject creates a new project in the given organization. An entitlement
// or suspension rejection is translated to ErrProjectLimit /
// ErrSubscriptionSuspended (errors.Is-matchable) so the caller can offer the
// right next step.
func (c *Client) CreateProject(ctx context.Context, in CreateProjectInput) (Project, error) {
	platform := in.Platform
	if platform == "" {
		platform = "CONTROL4"
	}
	input := map[string]any{
		"name":           in.Name,
		"organizationId": in.OrganizationID,
		"platform":       platform,
	}
	if in.Slug != "" {
		input["slug"] = in.Slug
	}
	if in.Description != "" {
		input["description"] = in.Description
	}

	var data struct {
		CreateProject Project `json:"createProject"`
	}
	if err := c.do(ctx, createProjectMutation, map[string]any{"input": input}, &data); err != nil {
		var ge *GraphError
		if errors.As(err, &ge) {
			switch {
			case ge.Code == "ENTITLEMENT_REQUIRED" || strings.Contains(ge.Message, "canCreateProject"):
				return Project{}, fmt.Errorf("%w: %s", ErrProjectLimit, ge.Message)
			case ge.Code == "SUBSCRIPTION_SUSPENDED":
				return Project{}, fmt.Errorf("%w: %s", ErrSubscriptionSuspended, ge.Message)
			}
		}
		return Project{}, err
	}
	return data.CreateProject, nil
}

// Platform is an automation platform Driverforge targets, with its availability. The
// graph is the single source of truth (shared with the web app) so the CLI's
// platform list never drifts.
type Platform struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Supported  bool   `json:"supported"`
	ComingSoon bool   `json:"comingSoon"`
	Hidden     bool   `json:"hidden"`
}

const platformsQuery = `query { platforms { id name supported comingSoon hidden } }`

// ListPlatforms returns the automation platforms Driverforge knows about.
func (c *Client) ListPlatforms(ctx context.Context) ([]Platform, error) {
	var data struct {
		Platforms []Platform `json:"platforms"`
	}
	if err := c.do(ctx, platformsQuery, nil, &data); err != nil {
		return nil, err
	}
	return data.Platforms, nil
}

const createPlatformInterestMutation = `mutation($input: CreatePlatformInterestInput!) { createPlatformInterest(input: $input) { id platform } }`

// CreatePlatformInterest registers the user's interest in a not-yet-available
// platform for the given organization (the "notify me" path).
func (c *Client) CreatePlatformInterest(ctx context.Context, organizationID, platform string) error {
	vars := map[string]any{"input": map[string]any{
		"organizationId": organizationID,
		"platform":       platform,
	}}
	return c.do(ctx, createPlatformInterestMutation, vars, nil)
}

const centrifugoTokenQuery = `query { centrifugoToken }`

// CentrifugoToken mints a short-lived connection token for the authenticated
// user, so the CLI can connect to the same real-time channel the web app uses to
// receive live driver telemetry. The method value satisfies the token-getter the
// telemetry watcher expects. Channel-level authorization is enforced separately
// by the server's subscribe proxy (a Project:view check), not by this token.
func (c *Client) CentrifugoToken(ctx context.Context) (string, error) {
	var data struct {
		CentrifugoToken string `json:"centrifugoToken"`
	}
	if err := c.do(ctx, centrifugoTokenQuery, nil, &data); err != nil {
		return "", err
	}
	return data.CentrifugoToken, nil
}

// Release returns the graph service's deploy stamp ("sha-<short_sha>") from
// GET /health/live — the APP_RELEASE the running build carries, for
// troubleshooting which graph build the CLI was talking to. Unauthenticated
// (a liveness endpoint). Empty (no error) when the service is unstamped
// (local dev deployments).
func (c *Client) Release(ctx context.Context) (string, error) {
	url := strings.TrimSuffix(c.endpoint, "/graphql") + "/health/live"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("graph api: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("graph api: health: HTTP %d", resp.StatusCode)
	}
	var body struct {
		Release *string `json:"release"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return "", fmt.Errorf("graph api: health: %w", err)
	}
	if body.Release == nil {
		return "", nil
	}
	return sanitize.Terminal(*body.Release), nil
}
