// Package driverforgeconfig reads and writes .driverforge/config.json — the committed record
// of a driver's Driverforge integration that `driverforge init` writes. It is the explicit
// "this project is set up for Driverforge" marker (replacing fragile source-sniffing)
// and carries the metadata re-runs need: the linked project, the embedded SDK
// version, build targets, and logger. It holds NO secrets — the API key is
// inlined in the driver; only the project reference is recorded here, so the file
// is safe to commit and share across a team.
package driverforgeconfig

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/driverforge/cli/internal/clierr"
)

// Schema is the current config schema version, bumped on incompatible changes.
const Schema = 1

const (
	dirName  = ".driverforge"
	fileName = "config.json"
)

// Project is the linked Driverforge project reference, by slug (no UUIDs, no secrets).
// Name may lazily equal the slug until the real name is fetched from the graph.
type Project struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// Config is the on-disk .driverforge/config.json document. It records org/project by
// slug — UUIDs are an internal graph detail the CLI resolves on demand, so they
// aren't persisted here.
type Config struct {
	Schema       int      `json:"schema"`
	Org          string   `json:"org"` // organization slug
	Project      Project  `json:"project"`
	SDKVersion   string   `json:"sdkVersion"`
	BuildTargets []string `json:"buildTargets"`
	Logger       string   `json:"logger,omitempty"`
	Entry        string   `json:"entry"`
	// Versioning is how driverforge bumps the driver.xml <version> ("date" or
	// "integer" — see internal/versioning), chosen during the init interview.
	Versioning string `json:"versioning,omitempty"`
	// Configurations attaches per-build-configuration defaults, keyed by
	// configuration name (the <name> in src/config.<name>.lua). It is a defaults
	// overlay, never a requirement: which configurations exist is decided by the
	// config.<name>.lua files alone, an entry here with no matching file is
	// inert, and flags always override (flag > config > authored source).
	Configurations map[string]Configuration `json:"configurations,omitempty"`
}

// Configuration is the defaults overlay for one named build configuration.
// Nil fields mean "no opinion" — the authored source or built-in default
// applies, so an empty entry changes nothing.
type Configuration struct {
	// Encrypt, when set, is the build's default script encryption (the
	// --encrypt tri-state): true always encrypts this configuration, false
	// never does, nil follows driver.xml as authored.
	Encrypt *bool `json:"encrypt,omitempty"`
	// Suffix, when false, opts this configuration out of the "-<name>" artifact
	// and " (<name>)" display-name suffixes so it builds the naked release
	// name — colliding (deliberately) with the default build's output path.
	// Nil/true keeps the suffixes.
	Suffix *bool `json:"suffix,omitempty"`
}

// Dir returns the .driverforge directory inside projectRoot.
func Dir(projectRoot string) string { return filepath.Join(projectRoot, dirName) }

// Path returns the .driverforge/config.json path inside projectRoot.
func Path(projectRoot string) string { return filepath.Join(Dir(projectRoot), fileName) }

// Load reads .driverforge/config.json. It returns (nil, nil) when the file is absent,
// so callers can treat "not initialised" distinctly from a read/parse error.
func Load(projectRoot string) (*Config, error) {
	data, err := os.ReadFile(Path(projectRoot))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		// The file is hand-editable and committed, so broken JSON (merge
		// conflict markers, trailing commas) is an anticipated condition.
		return nil, &clierr.UserError{
			Code:    clierr.CodeInvalidConfig,
			Title:   "Corrupt .driverforge/config.json",
			Message: "couldn't parse " + Path(projectRoot),
			Hint:    "fix the JSON (check for merge-conflict markers), or delete the file and re-run `driverforge init`",
			Err:     err,
		}
	}
	return &c, nil
}

// Marshal renders config bytes (pretty-printed, trailing newline). It stamps the
// current Schema and normalises a nil BuildTargets to an empty array so the JSON
// is stable.
func Marshal(c Config) ([]byte, error) {
	c.Schema = Schema
	if c.BuildTargets == nil {
		c.BuildTargets = []string{}
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
