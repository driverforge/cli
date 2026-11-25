package telemetry

import "testing"

func TestSetupURL(t *testing.T) {
	t.Setenv("DRIVERFORGE_APP_URL", "https://app.example.com")
	got := SetupURL("suru-pty-ltd", "pool-pump", "cli-starter-kit", "")
	if want := "https://app.example.com/suru-pty-ltd?from=cli-starter-kit&project=pool-pump"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := SetupURL("o", "p", "", ""); got != "https://app.example.com/o?project=p" {
		t.Errorf("without from: %q", got)
	}
	if got := SetupURL("o", "p", "cli-starter-kit", "drivers/my pump"); got != "https://app.example.com/o?dir=drivers%2Fmy+pump&from=cli-starter-kit&project=p" {
		t.Errorf("with dir: %q", got)
	}
}
