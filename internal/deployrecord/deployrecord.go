// Package deployrecord remembers what this CLI last deployed, so telemetry can
// be read back for exactly that deploy (`driverforge logs --version current`).
//
// One record is kept per (project, driver file, device): deploying a project's
// driver to a controller replaces that controller's record for it. Records live
// in the user's config directory beside the org context (deployments.json),
// not in the repo, because they describe this machine's deploys and must not be
// committed or shared. An ephemeral CI runner never has one and passes
// --version explicitly instead.
package deployrecord

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// schemaVersion is bumped on an incompatible change to the file. Fields are
// only ever added, so a newer CLI reads an older file unchanged.
const schemaVersion = 1

// Device identifies the controller a driver was deployed to.
type Device struct {
	ID       string `json:"id"`                 // graph device id
	Name     string `json:"name"`               // display name at deploy time
	DeviceID string `json:"deviceId,omitempty"` // hardware id (MAC): the telemetry controller
}

// Record is one successful deploy.
type Record struct {
	Org        string    `json:"org"`     // organization slug
	Project    string    `json:"project"` // project slug
	Driver     string    `json:"driver"`  // .c4z filename: the telemetry driver
	Device     Device    `json:"device"`
	Version    string    `json:"version"` // driver.xml <version> as deployed
	DeployedAt time.Time `json:"deployedAt"`
}

type file struct {
	SchemaVersion int               `json:"schemaVersion"`
	Records       map[string]Record `json:"records"`
}

// path is the records file; replaceable in tests.
var path = func() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "driverforge", "deployments.json")
}

func key(project, driver, deviceID string) string {
	return project + "\x00" + driver + "\x00" + deviceID
}

func load() file {
	f := file{SchemaVersion: schemaVersion, Records: map[string]Record{}}
	p := path()
	if p == "" {
		return f
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return f
	}
	_ = json.Unmarshal(data, &f) // a corrupt file reads as empty, like a missing one
	if f.Records == nil {
		f.Records = map[string]Record{}
	}
	return f
}

// Save records a deploy, replacing any earlier record for the same project,
// driver and device.
func Save(r Record) error {
	p := path()
	if p == "" {
		return fmt.Errorf("deployrecord: no user config directory")
	}
	f := load()
	f.SchemaVersion = schemaVersion
	f.Records[key(r.Project, r.Driver, r.Device.ID)] = r
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("deployrecord: %w", err)
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("deployrecord: %w", err)
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		return fmt.Errorf("deployrecord: %w", err)
	}
	return nil
}

// Latest returns the most recent deploy of any of the project's drivers to the
// device (by graph device id). A project builds one driver file per build
// configuration, so the newest deploy names the one that is running.
func Latest(project, deviceID string) (Record, bool) {
	var (
		best  Record
		found bool
	)
	for _, r := range load().Records {
		if r.Project != project || r.Device.ID != deviceID {
			continue
		}
		if !found || r.DeployedAt.After(best.DeployedAt) {
			best, found = r, true
		}
	}
	return best, found
}
