// Package testzip builds in-memory zip archives for tests.
//
// It exists because sdkrelease and agentrelease each need to fabricate the
// archive shape their release store serves, and a test helper in one package
// cannot be imported by another. Only _test.go files import this, so it is
// never linked into the CLI binary.
package testzip

import (
	"archive/zip"
	"bytes"
	"testing"
)

// WithFile returns a zip archive containing a single named entry.
func WithFile(t testing.TB, name, content string) []byte {
	t.Helper()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatalf("creating zip entry %q: %v", name, err)
	}
	if _, err := w.Write([]byte(content)); err != nil {
		t.Fatalf("writing zip entry %q: %v", name, err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("closing zip: %v", err)
	}
	return buf.Bytes()
}
