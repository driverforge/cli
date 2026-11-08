package remote4

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/driverforge/cli/internal/sdkrelease"
)

var (
	knownOnce sync.Once
	known     map[string]bool
)

// KnownCallbacks is the set of Director callback names the SDK instruments
// (vendored from its EventHandlers table). It is what separates a handler
// from any other top-level function in a driver.
func KnownCallbacks() map[string]bool {
	knownOnce.Do(func() {
		known = map[string]bool{}
		for _, line := range strings.Split(string(asset("callbacks.txt")), "\n") {
			if name := strings.TrimSpace(line); name != "" {
				known[name] = true
			}
		}
	})
	return known
}

// Top-level definitions of a global function, in either spelling. Anchored
// at line start so a method (`function Log:Info`) or a local never matches.
var (
	funcStmt   = regexp.MustCompile(`(?m)^[ \t]*function[ \t]+([A-Za-z_][A-Za-z0-9_]*)[ \t]*\(`)
	funcAssign = regexp.MustCompile(`(?m)^[ \t]*([A-Za-z_][A-Za-z0-9_]*)[ \t]*=[ \t]*function[ \t]*\(`)
)

// ScanHandlers walks the project's Lua sources for top-level definitions of
// known Director callbacks. It runs before the driver does — the driver cannot
// run until the stub is live — so it is static and may over-approximate (a
// handler defined inside a comment, say); the stub forwarding a callback the
// driver turns out not to handle is harmless. Vendored libraries are scanned —
// a framework such as drivers-common-public defines the driver's real
// handlers — except the Driverforge SDK, which defines a wrapper for every
// callback.
func ScanHandlers(srcDir string) ([]string, error) {
	found := map[string]bool{}
	err := filepath.WalkDir(srcDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != srcDir && (strings.HasPrefix(name, ".") || name == "dist") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".lua") || d.Name() == sdkrelease.BundleName {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, re := range []*regexp.Regexp{funcStmt, funcAssign} {
			for _, m := range re.FindAllSubmatch(src, -1) {
				if name := string(m[1]); KnownCallbacks()[name] {
					found[name] = true
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(found))
	for name := range found {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// Missing returns the names in have that are not in stub — handlers the
// running driver defines that the deployed stub will never forward.
func Missing(stub, have []string) []string {
	set := map[string]bool{}
	for _, s := range stub {
		set[s] = true
	}
	var out []string
	for _, h := range have {
		if KnownCallbacks()[h] && !set[h] {
			out = append(out, h)
		}
	}
	sort.Strings(out)
	return out
}
