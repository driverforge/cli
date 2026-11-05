// Package userconfig reads and writes the CLI's own JSON files in the user's
// config directory (driverforge/<name>): settings it keeps between runs, such
// as the selected organization and the metrics choice. They are best-effort by
// design — a file that is missing or unreadable reads as the zero value, so a
// damaged settings file never stops a command.
package userconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Path is the path of the named file, or "" when the user has no config
// directory.
func Path(name string) string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "driverforge", name)
}

// Load reads the named file into a T, or returns the zero T when the file is
// missing, unreadable or not valid JSON.
func Load[T any](name string) T {
	var v T
	path := Path(name)
	if path == "" {
		return v
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return v
	}
	if err := json.Unmarshal(data, &v); err != nil {
		var zero T
		return zero
	}
	return v
}

// Save writes v to the named file as indented JSON, readable only by the user
// (the files carry account context). It does nothing when there is no config
// directory.
func Save(name string, v any) error {
	path := Path(name)
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
