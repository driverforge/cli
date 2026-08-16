package auth

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/driverforge/cli/internal/clierr"
)

// keyringService is the service name under which credentials are stored in the
// OS secret store. The account is the auth host so multiple environments
// coexist (see authHost).
const keyringService = "driverforge-cli"

// ErrNotLoggedIn is returned by the store and session when no credentials are
// present. It is a clierr.UserError so it renders as a friendly card (and is
// never reported to Sentry) wherever it surfaces; callers still match it with
// errors.Is.
var ErrNotLoggedIn = &clierr.UserError{
	Title:   "Not logged in",
	Message: "you're not logged in",
	Hint:    "run `driverforge login`",
}

// Credentials is the persisted token bundle. The refresh token is the
// long-lived secret; the access token is short-lived (~30m). Subject/Email/Name
// are decoded from the token for display (whoami) and carry no authority.
type Credentials struct {
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken,omitempty"`
	Expiry       time.Time `json:"expiry,omitempty"`
	Subject      string    `json:"subject,omitempty"`
	Email        string    `json:"email,omitempty"`
	Name         string    `json:"name,omitempty"`
	Issuer       string    `json:"issuer,omitempty"`
	// AudienceTokens caches audience-narrowed access tokens across invocations,
	// keyed by audience. Minting one is a ~800ms refresh-token grant, so without
	// this every `deploy`/`sync` process pays it on its first authed call. They
	// are shorter-lived and less powerful than the RefreshToken already in this
	// bundle, so persisting them here adds no meaningful exposure.
	AudienceTokens map[string]AudienceToken `json:"audienceTokens,omitempty"`
}

// AudienceToken is a persisted audience-narrowed access token and its expiry.
type AudienceToken struct {
	AccessToken string    `json:"accessToken"`
	Expiry      time.Time `json:"expiry"`
}

// backend abstracts where credentials live so we can prefer the OS keychain and
// fall back to a file.
type backend interface {
	get(account string) (string, error)
	set(account, secret string) error
	del(account string) error
}

// selectBackend chooses the storage backend. DRIVERFORGE_CREDENTIALS_BACKEND forces
// "keyring" or "file"; "auto" (default) uses the keychain when it works and the
// file otherwise (headless Linux, containers, CI, bare SSH).
func selectBackend() backend {
	switch os.Getenv("DRIVERFORGE_CREDENTIALS_BACKEND") {
	case "file":
		return fileBackend{}
	case "keyring":
		return keyringBackend{}
	default:
		if keyringUsable() {
			return keyringBackend{}
		}
		return fileBackend{}
	}
}

// Save persists the credentials.
func Save(c Credentials) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return selectBackend().set(authHost(), string(data))
}

// Load returns the stored credentials, or ErrNotLoggedIn if none exist.
func Load() (Credentials, error) {
	raw, err := selectBackend().get(authHost())
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return Credentials{}, ErrNotLoggedIn
		}
		return Credentials{}, err
	}
	var c Credentials
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return Credentials{}, err
	}
	return c, nil
}

// Delete removes the stored credentials. It is idempotent.
func Delete() error {
	err := selectBackend().del(authHost())
	if errors.Is(err, keyring.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// --- keyring backend (macOS Keychain / Windows Credential Manager / Linux Secret Service) ---

type keyringBackend struct{}

func (keyringBackend) get(account string) (string, error) {
	return keyring.Get(keyringService, account)
}
func (keyringBackend) set(account, secret string) error {
	return keyring.Set(keyringService, account, secret)
}
func (keyringBackend) del(account string) error {
	return keyring.Delete(keyringService, account)
}

// keyringUsable reports whether the OS secret store works, probing at most
// once per process: a transient keychain hiccup mid-command must not silently
// flip Load to the file backend (which would report "not logged in" despite
// valid keychain credentials), and every Load/Save/Delete re-probing meant two
// extra keychain writes per operation.
var keyringUsable = sync.OnceValue(func() bool {
	const probe = "driverforge-keyring-probe"
	if err := keyring.Set(keyringService, probe, "1"); err != nil {
		return false
	}
	_ = keyring.Delete(keyringService, probe)
	return true
})

// --- file backend (0600 ~/.config/driverforge/credentials-<host>.json) ---

type fileBackend struct{}

// credentialsPath is keyed by account (the auth host), mirroring the keyring
// backend, so multiple environments coexist in file mode too — switching
// DRIVERFORGE_AUTH_URL must never return or overwrite another environment's tokens.
// It mirrors internal/metrics/config.go's use of os.UserConfigDir.
func credentialsPath(account string) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "driverforge", "credentials-"+safeAccount(account)+".json"), nil
}

// safeAccount maps an account (a host[:port]) to a filename component.
func safeAccount(account string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
			return r
		default:
			return '_'
		}
	}, account)
}

func (fileBackend) get(account string) (string, error) {
	path, err := credentialsPath(account)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// set writes the secret via temp-file + rename: atomic (a crash never leaves a
// half-written token file), and the 0600 mode is exact even when overwriting a
// pre-existing file with looser permissions (os.WriteFile would keep them).
func (fileBackend) set(account, secret string) error {
	path, err := credentialsPath(account)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".credentials-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op once renamed
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(secret); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (fileBackend) del(account string) error {
	path, err := credentialsPath(account)
	if err != nil {
		return err
	}
	return os.Remove(path)
}
