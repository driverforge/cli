package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func serveTemplates(t *testing.T, entries map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range entries {
		tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))})
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write(buf.Bytes()) }))
	t.Cleanup(srv.Close)
	t.Setenv("DRIVERFORGE_TEMPLATES_BASE_URL", srv.URL)
}

func TestNewNoInit(t *testing.T) {
	serveTemplates(t, map[string]string{
		"r-main/driver/template.json":     `{"variables":{"vendor":{"default":"Acme"}}}`,
		"r-main/driver/src/${id}.c4zproj": "proj ${title}",
		"r-main/driver/src/driver.lua":    "-- ${name} by ${vendor}",
	})
	t.Chdir(t.TempDir())
	var out bytes.Buffer
	doc, err := runNew(t.Context(), &out, "my-pool-pump", newOpts{repo: "o/r", noInit: true, set: map[string]string{"vendor": "Zed"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join("my-pool-pump", "src", "driver.lua"))
	if err != nil || string(got) != "-- my-pool-pump by Zed" {
		t.Fatalf("driver.lua = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join("my-pool-pump", "src", "my_pool_pump.c4zproj")); err != nil {
		t.Error(err)
	}
	if doc.Init != nil || len(doc.Files) != 2 {
		t.Errorf("doc = %+v", doc)
	}
	if _, err := runNew(t.Context(), &out, "my-pool-pump", newOpts{repo: "o/r", noInit: true}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("second run should refuse an existing directory, got %v", err)
	}
}
