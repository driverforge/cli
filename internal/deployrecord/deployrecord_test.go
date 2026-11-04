package deployrecord

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func useTempFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "driverforge", "deployments.json")
	orig := path
	path = func() string { return p }
	t.Cleanup(func() { path = orig })
	return p
}

func TestLatestWithoutRecord(t *testing.T) {
	useTempFile(t)
	if _, ok := Latest("smart-tv", "dev-1"); ok {
		t.Fatal("Latest found a record in an empty store")
	}
}

func TestSaveReplacesAndLatestPicksNewestDriver(t *testing.T) {
	p := useTempFile(t)
	t0 := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	dev := Device{ID: "dev-1", Name: "bench-a", DeviceID: "000FFF9BB74C"}

	for _, r := range []Record{
		{Project: "smart-tv", Driver: "smart_tv.c4z", Device: dev, Version: "11", DeployedAt: t0},
		{Project: "smart-tv", Driver: "smart_tv.c4z", Device: dev, Version: "12", DeployedAt: t0.Add(time.Minute)},
		// Another build configuration of the same project, deployed later.
		{Project: "smart-tv", Driver: "smart_tv-dev.c4z", Device: dev, Version: "13", DeployedAt: t0.Add(2 * time.Minute)},
		// Same project on another controller: never chosen for dev-1.
		{Project: "smart-tv", Driver: "smart_tv.c4z", Device: Device{ID: "dev-2"}, Version: "99", DeployedAt: t0.Add(time.Hour)},
	} {
		if err := Save(r); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	got, ok := Latest("smart-tv", "dev-1")
	if !ok || got.Version != "13" || got.Driver != "smart_tv-dev.c4z" {
		t.Fatalf("Latest = %+v, %v; want version 13 of smart_tv-dev.c4z", got, ok)
	}
	if f := load(); len(f.Records) != 3 {
		t.Errorf("records = %d, want 3 (the re-deploy replaces its predecessor)", len(f.Records))
	}
	if info, err := os.Stat(p); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v, %v; want 0600", info.Mode().Perm(), err)
	}
}

func TestCorruptFileReadsAsEmpty(t *testing.T) {
	p := useTempFile(t)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := Latest("smart-tv", "dev-1"); ok {
		t.Fatal("a corrupt file yielded a record")
	}
	if err := Save(Record{Project: "smart-tv", Driver: "d.c4z", Device: Device{ID: "dev-1"}, Version: "1"}); err != nil {
		t.Fatalf("Save over a corrupt file: %v", err)
	}
}
