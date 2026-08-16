package c4z

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The Progress stream is the build's public narration: the CLI's frames, step
// lines and plain output are all driven by the order and status of these events,
// yet nothing else in this package pins them. These tests do, so a change to the
// pipeline's structure has to be a deliberate change to what the user sees.

// statusName renders a Status for assertion messages. Status is an unnamed int
// with no String method, so a failure would otherwise read "3" rather than
// "fail".
var statusName = map[Status]string{StepStart: "start", StepOK: "ok", StepSkip: "skip", StepFail: "fail"}

// record runs a build and returns its events as "step/status" strings.
func record(t *testing.T, o Options) ([]string, error) {
	t.Helper()
	var got []string
	o.Progress = func(e Event) {
		got = append(got, string(e.Step)+"/"+statusName[e.Status])
	}
	_, err := Build(context.Background(), o)
	return got, err
}

func TestProgressSequenceSquishedAndEncrypted(t *testing.T) {
	_, srcDir, distDir := writeTestProject(t, true)

	got, err := record(t, Options{
		SrcDir: srcDir, DistDir: distDir, Sourcemap: true, Unpack: true,
		Now:       time.Date(2026, 6, 9, 14, 5, 0, 0, time.UTC),
		Encryptor: &recordingEncryptor{},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	want := []string{
		"resolve/start", "resolve/ok",
		"manifest/start", "manifest/ok",
		"version/skip",
		"prepare/start", "prepare/ok",
		"squish/start", "squish/ok",
		"driverxml/start", "driverxml/ok",
		"encrypt/start", "encrypt/ok",
		"pack/start", "pack/ok",
		"unpack/start", "unpack/ok",
		"sourcemap/start", "sourcemap/ok",
		"done/ok",
	}
	assertSteps(t, got, want)
}

func TestProgressSequenceSkipsWhatItDoesNotDo(t *testing.T) {
	_, srcDir, distDir := writeTestProject(t, false)
	// Not squished: squish is skipped, and a sourcemap cannot be produced.
	writeManifest(t, srcDir, false)

	got, err := record(t, Options{
		SrcDir: srcDir, DistDir: distDir, Sourcemap: true,
		Now: time.Date(2026, 6, 9, 14, 5, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	want := []string{
		"resolve/start", "resolve/ok",
		"manifest/start", "manifest/ok",
		"version/skip",
		"prepare/start", "prepare/ok",
		"squish/skip",
		"driverxml/start", "driverxml/ok",
		"encrypt/skip",
		"pack/start", "pack/ok",
		"sourcemap/skip",
		"done/ok",
	}
	assertSteps(t, got, want)
}

// A failure must narrate itself: the failing step reports fail, and nothing
// after it runs.
func TestProgressReportsTheFailingStep(t *testing.T) {
	_, srcDir, distDir := writeTestProject(t, true)

	got, err := record(t, Options{
		SrcDir: srcDir, DistDir: distDir,
		Now:       time.Date(2026, 6, 9, 14, 5, 0, 0, time.UTC),
		Encryptor: failingEncryptor{},
	})
	if err == nil {
		t.Fatal("expected the encryptor's failure to fail the build")
	}

	if last := got[len(got)-1]; last != "encrypt/fail" {
		t.Errorf("last event = %q, want encrypt/fail", last)
	}
	for _, e := range got {
		if strings.HasPrefix(e, "pack/") || strings.HasPrefix(e, "done/") {
			t.Errorf("build continued past the failure: %v", got)
			break
		}
	}
}

// A version bump is announced with the version it stamps.
func TestProgressAnnouncesTheStampedVersion(t *testing.T) {
	_, srcDir, distDir := writeTestProject(t, false)

	var msg string
	_, err := Build(context.Background(), Options{
		SrcDir: srcDir, DistDir: distDir, Version: "000042",
		Now: time.Date(2026, 6, 9, 14, 5, 0, 0, time.UTC),
		Progress: func(e Event) {
			if e.Step == StepVersion && e.Status == StepOK {
				msg = e.Msg
			}
		},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if msg != "000042" {
		t.Errorf("version ok event carried %q, want 000042", msg)
	}
}

type failingEncryptor struct{}

func (failingEncryptor) Encrypt([]byte) ([]byte, error) { return nil, errors.New("boom") }

func writeManifest(t *testing.T, srcDir string, squish bool) {
	t.Helper()
	sq := "false"
	if squish {
		sq = "true"
	}
	body := `<Driver type="c4z" name="test-driver" squishLua="` + sq + `">
  <Items>
    <Item type="file" name="driver.lua"/>
    <Item type="file" name="driver.xml"/>
  </Items>
</Driver>`
	if err := os.WriteFile(filepath.Join(srcDir, "manifest.c4zproj"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertSteps(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("event count = %d, want %d\ngot:  %v\nwant: %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d = %q, want %q\ngot:  %v\nwant: %v", i, got[i], want[i], got, want)
		}
	}
}
