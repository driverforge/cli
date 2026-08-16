package updatecheck

import (
	"strings"
	"testing"
)

// The upgrade hint is the only instruction a user gets, and they run it verbatim.
// A hint that is merely plausible — right tool, wrong invocation — sends them to a
// command that exits 0 having done nothing, which reads as "there is no update"
// rather than "you were told the wrong thing".

func TestHintForPathMatchesTheInstallLocation(t *testing.T) {
	for _, c := range []struct {
		name string
		exe  string
		goos string
		want string
	}{
		{
			name: "homebrew cask on apple silicon",
			exe:  "/opt/homebrew/Caskroom/driverforge/0.5.2/driverforge",
			goos: "darwin",
			want: "brew update && brew upgrade driverforge",
		},
		{
			name: "homebrew on intel macs",
			exe:  "/usr/local/Cellar/driverforge/0.5.2/bin/driverforge",
			goos: "darwin",
			want: "brew update && brew upgrade driverforge",
		},
		{
			name: "linuxbrew",
			exe:  "/home/linuxbrew/.linuxbrew/bin/driverforge",
			goos: "linux",
			want: "brew update && brew upgrade driverforge",
		},
		{
			name: "scoop",
			exe:  `C:\Users\dev\scoop\apps\driverforge\current\driverforge.exe`,
			goos: "windows",
			want: "scoop update && scoop update driverforge",
		},
		{
			name: "windows, installed some other way",
			exe:  `C:\Program Files\driverforge\driverforge.exe`,
			goos: "windows",
			want: "see https://go.driverforge.com/install",
		},
		{
			name: "installed by the shell installer",
			exe:  "/home/dev/.local/bin/driverforge",
			goos: "linux",
			want: `sh -c "$(curl -fsSL https://go.driverforge.com/get)"`,
		},
		{
			// os.Executable can fail; the hint still has to say something runnable.
			name: "unknown location",
			exe:  "",
			goos: "linux",
			want: `sh -c "$(curl -fsSL https://go.driverforge.com/get)"`,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := hintForPath(c.exe, c.goos); got != c.want {
				t.Errorf("hintForPath(%q, %q)\n got: %s\nwant: %s", c.exe, c.goos, got, c.want)
			}
		})
	}
}

// Both package managers compare against a local checkout of their manifests, so
// the upgrade command is a no-op until that checkout is refreshed. This is the
// regression that made `driverforge upgrade` print advice that did nothing.
func TestPackageManagerHintsRefreshTheirIndexFirst(t *testing.T) {
	for _, c := range []struct{ name, exe, refresh string }{
		{"homebrew", "/opt/homebrew/Caskroom/driverforge/0.5.2/driverforge", "brew update"},
		{"scoop", `C:\Users\dev\scoop\apps\driverforge\current\driverforge.exe`, "scoop update"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := hintForPath(c.exe, "")
			if !strings.HasPrefix(got, c.refresh+" &&") {
				t.Errorf("hint must refresh the index before upgrading; got %q, want it to start with %q &&", got, c.refresh)
			}
		})
	}
}
