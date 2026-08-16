// Package orgctx persists the user's active organization ("current org",
// like an Azure subscription) so org-scoped commands know which org to target.
//
// It lives in its own file (context.json) so it never clobbers the
// metrics-owned config.json, and separate from credentials so the two concerns
// stay decoupled. Logout clears it (see Clear) — a logged-out session has no
// active org. Mirrors internal/metrics/config.go.
package orgctx

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/driverforge/cli/internal/clierr"
)

const schemaVersion = 1

// ErrNoOrg is returned by Current when no organization has been selected. It is a
// clierr.UserError so it renders as a friendly card (never reported to Sentry);
// callers still match it with errors.Is.
var ErrNoOrg = &clierr.UserError{
	Title:   "No organization selected",
	Message: "no organization is selected",
	Hint:    "run `driverforge org select`",
}

// ErrNoDevice is returned by CurrentDevice when no device is selected for the
// current organization. Like ErrNoOrg, it is a friendly clierr.UserError.
var ErrNoDevice = &clierr.UserError{
	Title:   "No device selected",
	Message: "no device is selected",
	Hint:    "run `driverforge device select`",
}

// Org is the persisted identity of an organization. ID is the UUID the Graph
// API expects as organizationId; Slug is the human handle.
type Org struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// Device is the persisted identity of a selected deploy target (controller).
// OrgID scopes the selection: it is only valid while that org is active.
// Hostname/IP are cached so deploy/sync can reach the controller's agent without
// a graph round-trip (refresh via `driverforge device select` if the address changes).
type Device struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	OrgID          string `json:"orgId"`
	DeviceID       string `json:"deviceId,omitempty"` // hardware id / MAC, for re-querying the fingerprint
	Hostname       string `json:"hostname,omitempty"`
	IP             string `json:"ip,omitempty"`
	TLSFingerprint string `json:"tlsFingerprint,omitempty"` // pinned deploy-server cert fingerprint
}

// Context is the on-disk org context.
type Context struct {
	SchemaVersion int     `json:"schemaVersion"`
	CurrentOrg    *Org    `json:"currentOrg,omitempty"`
	CurrentDevice *Device `json:"currentDevice,omitempty"`
}

func contextPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "driverforge", "context.json")
}

// Load reads the context, returning a zero value if missing or unreadable.
func Load() Context {
	path := contextPath()
	if path == "" {
		return Context{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Context{}
	}
	var c Context
	if err := json.Unmarshal(data, &c); err != nil {
		return Context{}
	}
	return c
}

// Save writes the context to disk (0600).
func Save(c Context) error {
	path := contextPath()
	if path == "" {
		return nil
	}
	c.SchemaVersion = schemaVersion
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// Clear removes the saved org/device context, e.g. on logout. It is idempotent:
// clearing an already-empty context is a no-op.
func Clear() error {
	path := contextPath()
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// SetCurrent persists o as the current organization. Switching to a different
// org clears the selected device (device selection is org-scoped).
func SetCurrent(o Org) error {
	c := Load()
	if c.CurrentOrg == nil || c.CurrentOrg.ID != o.ID {
		c.CurrentDevice = nil
	}
	c.CurrentOrg = &o
	return Save(c)
}

// Current returns the selected organization, or ErrNoOrg if none is set.
func Current() (Org, error) {
	c := Load()
	if c.CurrentOrg == nil || c.CurrentOrg.ID == "" {
		return Org{}, ErrNoOrg
	}
	return *c.CurrentOrg, nil
}

// SetCurrentDevice persists d as the current deploy target.
func SetCurrentDevice(d Device) error {
	c := Load()
	c.CurrentDevice = &d
	return Save(c)
}

// CurrentDevice returns the selected device, but only if it belongs to the
// current organization; otherwise ErrNoDevice (this is the org-scoping guard
// that invalidates a stale selection after an org switch).
func CurrentDevice() (Device, error) {
	c := Load()
	if c.CurrentDevice == nil || c.CurrentDevice.ID == "" {
		return Device{}, ErrNoDevice
	}
	if c.CurrentOrg == nil || c.CurrentDevice.OrgID != c.CurrentOrg.ID {
		return Device{}, ErrNoDevice
	}
	return *c.CurrentDevice, nil
}

// ChooseDefault picks the default org id for selection: the remembered id if it
// is still among ids, otherwise the first id, otherwise "".
func ChooseDefault(ids []string, remembered string) string {
	for _, id := range ids {
		if id == remembered && remembered != "" {
			return remembered
		}
	}
	if len(ids) > 0 {
		return ids[0]
	}
	return ""
}
