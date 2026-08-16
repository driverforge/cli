// Package deploycache remembers the structural hash of the last driver build
// deployed/synced to a given controller, so `driverforge sync` can decide locally
// (without a round-trip) whether a fast hot-swap is safe or a full deploy is
// needed. The agent remains the source of truth; this is only an optimisation,
// refreshed from the hash each deploy/sync response echoes. It lives in the
// cache dir (regenerable), keyed by device id + c4z filename.
package deploycache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const schemaVersion = 1

type entry struct {
	StructuralHash string    `json:"structuralHash"`
	At             time.Time `json:"at"`
}

type cache struct {
	SchemaVersion int              `json:"schemaVersion"`
	Entries       map[string]entry `json:"entries"`
}

func cachePath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "driverforge", "deploys.json")
}

func key(deviceID, filename string) string { return deviceID + "\x00" + filename }

func load() cache {
	c := cache{SchemaVersion: schemaVersion, Entries: map[string]entry{}}
	p := cachePath()
	if p == "" {
		return c
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return c
	}
	_ = json.Unmarshal(data, &c)
	if c.Entries == nil {
		c.Entries = map[string]entry{}
	}
	return c
}

// Get returns the cached structural hash for (deviceID, filename), or "" if none.
func Get(deviceID, filename string) string {
	return load().Entries[key(deviceID, filename)].StructuralHash
}

// Set records the structural hash for (deviceID, filename). Errors are swallowed
// by callers — a cache miss only costs an extra round-trip.
func Set(deviceID, filename, structuralHash string) error {
	c := load()
	c.SchemaVersion = schemaVersion
	c.Entries[key(deviceID, filename)] = entry{StructuralHash: structuralHash, At: time.Now()}
	p := cachePath()
	if p == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}
