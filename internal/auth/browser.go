package auth

import (
	"os/exec"
	"runtime"
)

// OpenBrowser tries to open url in the user's default browser. It returns an
// error if the platform opener isn't found or fails; callers fall back to
// printing the URL. Hand-rolled to avoid a dependency.
func OpenBrowser(url string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name, args = "open", []string{url}
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default: // linux, bsd, etc.
		name, args = "xdg-open", []string{url}
	}
	return exec.Command(name, args...).Start()
}
