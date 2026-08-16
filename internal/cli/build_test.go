package cli

import (
	"testing"

	"github.com/driverforge/cli/internal/driverforgeconfig"
)

func boolp(v bool) *bool { return &v }

// configurationDefaults is the config half of the flag > config > authored
// precedence chain: it must have no opinion (nil encrypt, suffix on) whenever
// the project config, the configuration, or its entry is absent — the
// zero-config invariant — and surface the entry's values otherwise.
func TestConfigurationDefaults(t *testing.T) {
	prod := driverforgeconfig.Configuration{Encrypt: boolp(true), Suffix: boolp(false)}
	cfg := &driverforgeconfig.Config{
		Configurations: map[string]driverforgeconfig.Configuration{
			"prod":  prod,
			"dev":   {Encrypt: boolp(false)},
			"empty": {},
		},
	}

	tests := []struct {
		name          string
		cfg           *driverforgeconfig.Config
		configuration string
		wantEncrypt   *bool
		wantNoSuffix  bool
	}{
		{"no project config", nil, "prod", nil, false},
		{"default build ignores entries", cfg, "", nil, false},
		{"configuration without entry", cfg, "staging", nil, false},
		{"empty entry has no opinion", cfg, "empty", nil, false},
		{"encrypt only", cfg, "dev", boolp(false), false},
		{"encrypt and suffix opt-out", cfg, "prod", boolp(true), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encrypt, noSuffix := configurationDefaults(tt.cfg, tt.configuration)
			if (encrypt == nil) != (tt.wantEncrypt == nil) || (encrypt != nil && *encrypt != *tt.wantEncrypt) {
				t.Errorf("encrypt = %v, want %v", encrypt, tt.wantEncrypt)
			}
			if noSuffix != tt.wantNoSuffix {
				t.Errorf("noSuffix = %v, want %v", noSuffix, tt.wantNoSuffix)
			}
		})
	}
}
