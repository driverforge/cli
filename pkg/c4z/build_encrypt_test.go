package c4z

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writePlainTestProject writes a project that does NOT squish its Lua: the
// manifest omits squishLua and there is no squishy. driver.lua ships as authored.
func writePlainTestProject(t *testing.T, encryption bool) (srcDir, distDir string) {
	t.Helper()
	projectDir := t.TempDir()
	srcDir = filepath.Join(projectDir, "src")
	distDir = filepath.Join(projectDir, "dist")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(rel, content string) {
		if err := os.WriteFile(filepath.Join(srcDir, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	encAttr := ""
	if encryption {
		encAttr = ` encryption="2"`
	}
	write("driver.xml", strings.Replace(testDriverXMLTmpl, "%s", encAttr, 1))
	write("manifest.c4zproj", `<Driver type="c4z" name="plain-driver">
  <Items>
    <Item type="file" name="driver.lua"/>
    <Item type="file" name="driver.xml"/>
  </Items>
</Driver>`)
	write("driver.lua", plainDriverLua)
	return srcDir, distDir
}

const plainDriverLua = "-- not squished\nlocal secret = 'proprietary'\nprint(secret)\n"

// A driver that declares encryption but does NOT squish must encrypt the main
// script as authored. The squished path is covered elsewhere; this one reads the
// file off the work tree instead, and shipping its plaintext would leak source.
func TestBuildEncryptedNotSquished(t *testing.T) {
	srcDir, distDir := writePlainTestProject(t, true)
	rec := &recordingEncryptor{}

	res, err := Build(context.Background(), Options{
		SrcDir: srcDir, DistDir: distDir, Now: time.Now(), Encryptor: rec,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Encrypted {
		t.Error("Result.Encrypted = false, want true")
	}

	// The encryptor sees the authored file verbatim — no squish preamble.
	if string(rec.got) != plainDriverLua {
		t.Errorf("encryptor input = %q, want the authored driver.lua %q", rec.got, plainDriverLua)
	}

	entries := readZip(t, res.C4zPath)
	if string(entries["driver.lua.encrypted"]) != "ENCRYPTED" {
		t.Errorf("driver.lua.encrypted missing or wrong; entries: %v", keys(entries))
	}
	if _, ok := entries["driver.lua"]; ok {
		t.Error("plaintext driver.lua must not ship in an encrypted, non-squished c4z")
	}
	for name, body := range entries {
		if strings.Contains(string(body), "proprietary") {
			t.Errorf("%s carries the plaintext Lua source", name)
		}
	}
}

// --no-encrypt (Options.Encrypt = &false) must force encryption off even though
// the source driver.xml asks for it: the plaintext script ships and the packaged
// driver.xml must not claim encryption="2", or the Director would try to decrypt
// a plaintext file.
func TestBuildEncryptFlagForcesOff(t *testing.T) {
	_, srcDir, distDir := writeTestProject(t, true) // source declares encryption="2"
	rec := &recordingEncryptor{}
	off := false

	res, err := Build(context.Background(), Options{
		Configuration: "dev", SrcDir: srcDir, DistDir: distDir,
		Encrypt: &off, Now: time.Now(), Encryptor: rec,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Encrypted {
		t.Error("Result.Encrypted = true, want false with --no-encrypt")
	}
	if rec.got != nil {
		t.Errorf("the encryptor must not be called with --no-encrypt (got %q)", rec.got)
	}

	entries := readZip(t, res.C4zPath)
	if _, ok := entries["driver.lua.encrypted"]; ok {
		t.Error("an encrypted entry was packed despite --no-encrypt")
	}
	lua, ok := entries["driver.lua"]
	if !ok {
		t.Fatalf("plaintext driver.lua missing; entries: %v", keys(entries))
	}
	if !strings.Contains(string(lua), "package.preload['config']") {
		t.Errorf("driver.lua should be the squished bundle: %q", lua)
	}
	xml := string(entries["driver.xml"])
	if strings.Contains(xml, `encryption="2"`) {
		t.Errorf("packaged driver.xml still declares encryption:\n%s", xml)
	}

	// The override is build-only — the source must keep its encryption request.
	src, _ := os.ReadFile(filepath.Join(srcDir, "driver.xml"))
	if !strings.Contains(string(src), `encryption="2"`) {
		t.Errorf("--no-encrypt must not strip encryption from the source driver.xml:\n%s", src)
	}
}

// The same force-off on a non-squished driver: the authored driver.lua ships
// as-is, so this is the combination where a leak would be plainest.
func TestBuildEncryptFlagForcesOffNotSquished(t *testing.T) {
	srcDir, distDir := writePlainTestProject(t, true)
	rec := &recordingEncryptor{}
	off := false

	res, err := Build(context.Background(), Options{
		SrcDir: srcDir, DistDir: distDir, Encrypt: &off, Now: time.Now(), Encryptor: rec,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Encrypted {
		t.Error("Result.Encrypted = true, want false with --no-encrypt")
	}
	if rec.got != nil {
		t.Errorf("the encryptor must not be called with --no-encrypt (got %q)", rec.got)
	}

	entries := readZip(t, res.C4zPath)
	if string(entries["driver.lua"]) != plainDriverLua {
		t.Errorf("driver.lua = %q, want the authored source", entries["driver.lua"])
	}
	if _, ok := entries["driver.lua.encrypted"]; ok {
		t.Error("an encrypted entry was packed despite --no-encrypt")
	}
	if strings.Contains(string(entries["driver.xml"]), `encryption="2"`) {
		t.Errorf("packaged driver.xml still declares encryption:\n%s", entries["driver.xml"])
	}
}

// A non-squished driver with no encryption request at all is the baseline: the
// plaintext ships and nothing claims otherwise.
func TestBuildNotSquishedNotEncrypted(t *testing.T) {
	srcDir, distDir := writePlainTestProject(t, false)
	rec := &recordingEncryptor{}

	res, err := Build(context.Background(), Options{
		SrcDir: srcDir, DistDir: distDir, Now: time.Now(), Encryptor: rec,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Encrypted {
		t.Error("Result.Encrypted = true, want false")
	}
	if rec.got != nil {
		t.Errorf("the encryptor must not be called (got %q)", rec.got)
	}
	entries := readZip(t, res.C4zPath)
	if string(entries["driver.lua"]) != plainDriverLua {
		t.Errorf("driver.lua = %q, want the authored source", entries["driver.lua"])
	}
}
