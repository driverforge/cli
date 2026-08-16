// Package metrics implements opt-in, fire-and-forget usage telemetry for the
// driverforge CLI. Nothing is collected until the user consents (prompted once at
// first launch); collection can always be disabled via DO_NOT_TRACK or
// DRIVERFORGE_NO_METRICS. Events are sent to Mixpanel. All sending is
// best-effort: errors are swallowed and never affect a command's outcome or
// exit code.
//
// The build library (pkg/c4z) stays telemetry-free; instrumentation lives here
// and is driven from the CLI layer.
package metrics

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// schemaVersion is the on-disk config schema version, for future migrations.
const schemaVersion = 1

// Consent states persisted in the config.
const (
	ConsentUnset   = ""
	ConsentGranted = "granted"
	ConsentDenied  = "denied"
)

// Config is the durable, user-level driverforge configuration. It is distinct from
// the update-check cache: this is intentional, user-owned state.
type Config struct {
	SchemaVersion         int       `json:"schemaVersion"`
	AnonymousID           string    `json:"anonymousId"`
	MetricsConsent        string    `json:"metricsConsent"` // "", "granted", "denied"
	ConsentAskedAt        time.Time `json:"consentAskedAt,omitempty"`
	ErrorReportingConsent string    `json:"errorReportingConsent,omitempty"` // "", "granted", "denied"
}

// configPath returns the path to the driverforge config file, or "" if the user
// config dir cannot be determined.
func configPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "driverforge", "config.json")
}

// Load reads the config, returning a zero-value Config if it is missing or
// unreadable (errors are intentionally swallowed — telemetry config must never
// break a command).
func Load() Config {
	path := configPath()
	if path == "" {
		return Config{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return Config{}
	}
	return c
}

// Save writes the config to disk (0600, since it carries a stable anonymous id).
// Errors are returned but callers generally swallow them.
func Save(c Config) error {
	path := configPath()
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

// newAnonymousID returns a random, non-identifying id (16 random bytes, hex).
// Avoids a uuid dependency.
func newAnonymousID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand should never fail; if it does, derive a value from the clock
		// but hash it so the result is the same length and shape as the normal id
		// (32 hex chars) rather than a ~60-char, directly-guessable hex timestamp.
		sum := sha256.Sum256([]byte(time.Now().UTC().String()))
		return hex.EncodeToString(sum[:16])
	}
	return hex.EncodeToString(b[:])
}
