package c4z

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// recordingEncryptor captures the plaintext it is asked to encrypt and returns
// a recognisable marker, so tests can assert what gets encrypted without crypto.
type recordingEncryptor struct{ got []byte }

func (r *recordingEncryptor) Encrypt(plaintext []byte) ([]byte, error) {
	r.got = append([]byte(nil), plaintext...)
	return []byte("ENCRYPTED"), nil
}

const testDriverXMLTmpl = `<devicedata>
  <name>Test Driver</name>
  <modified>1/1/1970 00:00:00 AM</modified>
  <version>000001</version>
  <vendorext>1.2.3</vendorext>
  <config>
    <script jit="1"%s file="driver.lua"/>
  </config>
</devicedata>
`

func writeTestProject(t *testing.T, encryption bool) (projectDir, srcDir, distDir string) {
	t.Helper()
	projectDir = t.TempDir()
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
	write("manifest.c4zproj", `<Driver type="c4z" name="test-driver" squishLua="true">
  <Items>
    <Item type="file" name="driver.lua"/>
    <Item type="file" name="driver.xml"/>
  </Items>
</Driver>`)
	write("squishy", "Main \"driver.lua\"\nModule \"config\" \"config.lua\"\nOutput \"../dist/driver-squished.lua\"\n")
	write("config.lua", "return { real = true }\n")
	write("driver.lua", "local c = require('config')\nprint(c)\n")
	write("config.dev.lua", "return { env = 'dev' }\n")
	return projectDir, srcDir, distDir
}

func readZip(t *testing.T, path string) map[string][]byte {
	t.Helper()
	r, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	out := map[string][]byte{}
	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = data
	}
	return out
}

func TestBuildNonEncrypted(t *testing.T) {
	_, srcDir, distDir := writeTestProject(t, false)
	now := time.Date(2026, 6, 9, 14, 5, 0, 0, time.UTC)

	res, err := Build(context.Background(), Options{
		Configuration: "dev", SrcDir: srcDir, DistDir: distDir, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ArtifactName != "test-driver-dev" {
		t.Errorf("ArtifactName = %q, want test-driver-dev", res.ArtifactName)
	}
	if res.Version != "000001" {
		t.Errorf("Version = %q, want authored 000001 (passthrough)", res.Version)
	}

	entries := readZip(t, res.C4zPath)
	lua, ok := entries["driver.lua"]
	if !ok {
		t.Fatal("driver.lua missing from c4z")
	}
	// driver.lua must be the squished bundle: env config (config swap) wrapped in
	// a preload, then the main file verbatim.
	want := "package.preload['config'] = (function (...)\nreturn { env = 'dev' }\n end)\nlocal c = require('config')\nprint(c)\n"
	if string(lua) != want {
		t.Errorf("bundle mismatch:\n got %q\nwant %q", lua, want)
	}
	xml := string(entries["driver.xml"])
	// <version> exactly as authored; unmanaged elements pass through inert.
	for _, want := range []string{"<version>000001</version>", "<vendorext>1.2.3</vendorext>",
		"<modified>06/09/2026 02:05 PM</modified>", "<name>Test Driver (dev)</name>"} {
		if !strings.Contains(xml, want) {
			t.Errorf("driver.xml missing %q\n---\n%s", want, xml)
		}
	}
}

// TestBuildDefaultConfiguration covers an opt-out build: no --configuration means
// no config.<configuration>.lua is required, no config swap happens (the committed
// config.lua is used), and the artifact/display name carry no suffix.
func TestBuildDefaultConfiguration(t *testing.T) {
	_, srcDir, distDir := writeTestProject(t, false)
	// Remove the config file to prove it is not needed for a default build.
	if err := os.Remove(filepath.Join(srcDir, "config.dev.lua")); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 9, 14, 5, 0, 0, time.UTC)

	res, err := Build(context.Background(), Options{
		Configuration: "", SrcDir: srcDir, DistDir: distDir, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ArtifactName != "test-driver" {
		t.Errorf("ArtifactName = %q, want test-driver (no env suffix)", res.ArtifactName)
	}

	entries := readZip(t, res.C4zPath)
	// The committed config.lua (not an env file) must be used in the bundle.
	want := "package.preload['config'] = (function (...)\nreturn { real = true }\n end)\nlocal c = require('config')\nprint(c)\n"
	if got := string(entries["driver.lua"]); got != want {
		t.Errorf("bundle mismatch:\n got %q\nwant %q", got, want)
	}
	xml := string(entries["driver.xml"])
	if !strings.Contains(xml, "<name>Test Driver</name>") {
		t.Errorf("default build device name must have no suffix:\n%s", xml)
	}
	if strings.Contains(xml, "Test Driver (") {
		t.Errorf("default build device name must not be suffixed:\n%s", xml)
	}
}

// TestBuildNoSuffix covers a release-style configuration build: the config swap
// still happens, but the artifact keeps the naked name and the device display
// name carries no "(configuration)" decoration — deliberately sharing the
// default build's output path.
func TestBuildNoSuffix(t *testing.T) {
	_, srcDir, distDir := writeTestProject(t, false)
	now := time.Date(2026, 6, 9, 14, 5, 0, 0, time.UTC)

	res, err := Build(context.Background(), Options{
		Configuration: "dev", NoSuffix: true, SrcDir: srcDir, DistDir: distDir, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ArtifactName != "test-driver" {
		t.Errorf("ArtifactName = %q, want test-driver (naked)", res.ArtifactName)
	}
	if want := filepath.Join(distDir, "test-driver.c4z"); res.C4zPath != want {
		t.Errorf("C4zPath = %q, want %q (default build's path)", res.C4zPath, want)
	}

	entries := readZip(t, res.C4zPath)
	// The configuration's config swap must still apply — only the naming opts out.
	want := "package.preload['config'] = (function (...)\nreturn { env = 'dev' }\n end)\nlocal c = require('config')\nprint(c)\n"
	if got := string(entries["driver.lua"]); got != want {
		t.Errorf("bundle mismatch:\n got %q\nwant %q", got, want)
	}
	xml := string(entries["driver.xml"])
	if !strings.Contains(xml, "<name>Test Driver</name>") || strings.Contains(xml, "Test Driver (") {
		t.Errorf("no-suffix build device name must be undecorated:\n%s", xml)
	}
}

// TestBuildVanilla covers a pre-existing driver with no driverforge-specific changes:
// no extra elements, no env files, no package.json. It must build the way it always
// did — the authored <version> is preserved and no env config swap or name suffix
// is applied.
func TestBuildVanilla(t *testing.T) {
	_, srcDir, distDir := writeTestProject(t, false)
	// Strip the extra element and remove the env file to make it "vanilla".
	xmlPath := filepath.Join(srcDir, "driver.xml")
	data, err := os.ReadFile(xmlPath)
	if err != nil {
		t.Fatal(err)
	}
	stripped := strings.Replace(string(data), "  <vendorext>1.2.3</vendorext>\n", "", 1)
	if err := os.WriteFile(xmlPath, []byte(stripped), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(srcDir, "config.dev.lua")); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 9, 14, 5, 0, 0, time.UTC)

	res, err := Build(context.Background(), Options{
		Configuration: "", SrcDir: srcDir, DistDir: distDir, Now: now,
	})
	if err != nil {
		t.Fatalf("vanilla build failed: %v", err)
	}
	if res.ArtifactName != "test-driver" {
		t.Errorf("ArtifactName = %q, want test-driver", res.ArtifactName)
	}
	if res.Version != "000001" {
		t.Errorf("Version = %q, want authored 000001 (untouched)", res.Version)
	}

	xml := string(readZip(t, res.C4zPath)["driver.xml"])
	if !strings.Contains(xml, "<version>000001</version>") {
		t.Errorf("authored <version> must be preserved:\n%s", xml)
	}
	if !strings.Contains(xml, "<name>Test Driver</name>") {
		t.Errorf("device name must have no suffix:\n%s", xml)
	}
}

func TestBuildEncrypted(t *testing.T) {
	_, srcDir, distDir := writeTestProject(t, true)
	rec := &recordingEncryptor{}

	res, err := Build(context.Background(), Options{
		Configuration: "dev", SrcDir: srcDir, DistDir: distDir,
		Now: time.Now(), Encryptor: rec,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Encrypted {
		t.Error("Result.Encrypted = false, want true")
	}

	entries := readZip(t, res.C4zPath)
	if _, ok := entries["driver.lua.encrypted"]; !ok {
		t.Errorf("driver.lua.encrypted missing; entries: %v", keys(entries))
	}
	if _, ok := entries["driver.lua"]; ok {
		t.Error("plaintext driver.lua must not be present in an encrypted c4z")
	}
	if string(entries["driver.lua.encrypted"]) != "ENCRYPTED" {
		t.Error("encrypted entry does not contain the encryptor output")
	}
	// The encryptor must receive the squished bundle, not the raw main file.
	if !strings.Contains(string(rec.got), "package.preload['config']") {
		t.Errorf("encryptor input was not the squished bundle: %q", rec.got)
	}
}

// TestBuildEncryptFlagForcesOn covers --encrypt on a driver whose source
// driver.xml does NOT request encryption: the build encrypts anyway (without
// mutating the source), and the packaged driver.xml declares encryption="2".
func TestBuildEncryptFlagForcesOn(t *testing.T) {
	_, srcDir, distDir := writeTestProject(t, false) // no encryption in source
	rec := &recordingEncryptor{}
	on := true

	res, err := Build(context.Background(), Options{
		Configuration: "dev", SrcDir: srcDir, DistDir: distDir,
		Encrypt: &on, Now: time.Now(), Encryptor: rec,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Encrypted {
		t.Error("Result.Encrypted = false, want true with --encrypt")
	}

	entries := readZip(t, res.C4zPath)
	if _, ok := entries["driver.lua.encrypted"]; !ok {
		t.Errorf("driver.lua.encrypted missing; entries: %v", keys(entries))
	}
	if _, ok := entries["driver.lua"]; ok {
		t.Error("plaintext driver.lua must not be present when encrypting")
	}
	if !strings.Contains(string(entries["driver.xml"]), `encryption="2"`) {
		t.Errorf("packaged driver.xml should declare encryption=\"2\":\n%s", entries["driver.xml"])
	}

	// The source driver.xml on disk must remain untouched (no encryption added).
	src, _ := os.ReadFile(filepath.Join(srcDir, "driver.xml"))
	if strings.Contains(string(src), `encryption="2"`) {
		t.Errorf("--encrypt must not modify the source driver.xml:\n%s", src)
	}
}

func keys(m map[string][]byte) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

// --encrypt on a driver.xml with no <script file="..."> tag must fail loudly:
// there is nothing to encrypt, and a silent plaintext build would betray the
// explicit request.
func TestBuildEncryptForcedWithNoScriptTag(t *testing.T) {
	projectDir := t.TempDir()
	srcDir := filepath.Join(projectDir, "src")
	distDir := filepath.Join(projectDir, "dist")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(rel, content string) {
		if err := os.WriteFile(filepath.Join(srcDir, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("driver.xml", `<devicedata>
  <name>Scriptless Driver</name>
  <version>000001</version>
</devicedata>
`)
	write("manifest.c4zproj", `<Driver type="c4z" name="scriptless-driver">
  <Items>
    <Item type="file" name="driver.xml"/>
    <Item type="file" name="helper.lua"/>
  </Items>
</Driver>`)
	write("helper.lua", "return {}\n")

	on := true
	_, err := Build(context.Background(), Options{
		SrcDir: srcDir, DistDir: distDir, Encrypt: &on,
	})
	var noScript *NoEncryptableScriptError
	if !errors.As(err, &noScript) {
		t.Fatalf("Build error = %v, want NoEncryptableScriptError", err)
	}
	if _, statErr := os.Stat(filepath.Join(distDir, "scriptless-driver.c4z")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("a .c4z was produced despite the failed encryption request (stat: %v)", statErr)
	}
}

// A build that fails after the version step must NOT leave the source
// driver.xml bumped — retrying after fixing the problem would double-bump.
func TestBuildFailureDoesNotPersistVersionBump(t *testing.T) {
	_, srcDir, distDir := writeTestProject(t, false)
	// The manifest declares squishLua; removing the squishy makes StepSquish
	// fail after StepVersion has run.
	if err := os.Remove(filepath.Join(srcDir, "squishy")); err != nil {
		t.Fatal(err)
	}

	_, err := Build(context.Background(), Options{
		SrcDir: srcDir, DistDir: distDir, Version: "2026070601", Now: time.Now(),
	})
	if err == nil {
		t.Fatal("expected the build to fail without a squishy")
	}
	src, _ := os.ReadFile(filepath.Join(srcDir, "driver.xml"))
	if !strings.Contains(string(src), "<version>000001</version>") {
		t.Errorf("source driver.xml must keep 000001 after a failed build:\n%s", src)
	}
}

// An unsupported manifest type must be rejected before versioning runs, so a
// version stamp on a c4i driver doesn't mutate the source tree.
func TestBuildVersionUnsupportedTypeDoesNotStamp(t *testing.T) {
	_, srcDir, distDir := writeTestProject(t, false)
	if err := os.WriteFile(filepath.Join(srcDir, "manifest.c4zproj"), []byte(`<Driver type="c4i" name="test-driver">
  <Items>
    <Item type="file" name="driver.xml"/>
  </Items>
</Driver>`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Build(context.Background(), Options{
		SrcDir: srcDir, DistDir: distDir, Version: "2026070601", Now: time.Now(),
	})
	var unsupported *UnsupportedDriverTypeError
	if !errors.As(err, &unsupported) {
		t.Fatalf("error = %v, want UnsupportedDriverTypeError", err)
	}
	src, _ := os.ReadFile(filepath.Join(srcDir, "driver.xml"))
	if !strings.Contains(string(src), "<version>000001</version>") {
		t.Errorf("source driver.xml must be untouched after a rejected build:\n%s", src)
	}
}

// --version stamps an absolute value verbatim (driverpackager --driver-version
// parity): the artifact and the persisted source both carry it; unmanaged
// elements are left untouched and do not overwrite the explicit value.
func TestBuildAbsoluteVersion(t *testing.T) {
	_, srcDir, distDir := writeTestProject(t, false)

	res, err := Build(context.Background(), Options{
		SrcDir: srcDir, DistDir: distDir, Version: "2026070501", Now: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Version != "2026070501" {
		t.Errorf("Version = %q, want 2026070501", res.Version)
	}
	xml := string(readZip(t, res.C4zPath)["driver.xml"])
	if !strings.Contains(xml, "<version>2026070501</version>") {
		t.Errorf("packaged driver.xml missing the absolute version:\n%s", xml)
	}
	if !strings.Contains(xml, "<vendorext>1.2.3</vendorext>") {
		t.Errorf("unmanaged elements must be left as authored:\n%s", xml)
	}
	src, _ := os.ReadFile(filepath.Join(srcDir, "driver.xml"))
	if !strings.Contains(string(src), "<version>2026070501</version>") {
		t.Errorf("source driver.xml not persisted with the absolute version:\n%s", src)
	}
}

// --allow-execute appends the Lua-command-window lines to the packaged script
// (squished: at the bundle tail) without ever touching the source.
func TestBuildAllowExecuteSquished(t *testing.T) {
	_, srcDir, distDir := writeTestProject(t, false)

	res, err := Build(context.Background(), Options{
		SrcDir: srcDir, DistDir: distDir, AllowExecute: true, Now: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	lua := string(readZip(t, res.C4zPath)["driver.lua"])
	if !strings.Contains(lua, "C4:AllowExecute(true)") || !strings.Contains(lua, "gIsDevelopmentVersionOfDriver = true") {
		t.Errorf("packaged bundle missing allow-execute lines:\n%s", lua)
	}
	src, _ := os.ReadFile(filepath.Join(srcDir, "driver.lua"))
	if strings.Contains(string(src), "AllowExecute") {
		t.Errorf("source driver.lua must never be modified:\n%s", src)
	}
}

func TestBuildAllowExecuteNonSquished(t *testing.T) {
	_, srcDir, distDir := writeTestProject(t, false)
	// Drop squishing: plain file items only.
	if err := os.WriteFile(filepath.Join(srcDir, "manifest.c4zproj"), []byte(`<Driver type="c4z" name="test-driver">
  <Items>
    <Item type="file" name="driver.lua"/>
    <Item type="file" name="driver.xml"/>
  </Items>
</Driver>`), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Build(context.Background(), Options{
		SrcDir: srcDir, DistDir: distDir, AllowExecute: true, Now: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	lua := string(readZip(t, res.C4zPath)["driver.lua"])
	if !strings.Contains(lua, "C4:AllowExecute(true)") {
		t.Errorf("packaged driver.lua missing allow-execute lines:\n%s", lua)
	}
	src, _ := os.ReadFile(filepath.Join(srcDir, "driver.lua"))
	if strings.Contains(string(src), "AllowExecute") {
		t.Errorf("source driver.lua must never be modified:\n%s", src)
	}
}

// A custom manifest path and output directory (driverpackager srcdir/dstdir/
// manifest parity): the manifest need not live at src/manifest.c4zproj and the
// artifact lands where asked.
func TestBuildCustomManifestPathAndOutputDir(t *testing.T) {
	projectDir, srcDir, _ := writeTestProject(t, false)
	altManifest := filepath.Join(projectDir, "alt.c4zproj")
	data, err := os.ReadFile(filepath.Join(srcDir, "manifest.c4zproj"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(altManifest, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(srcDir, "manifest.c4zproj")); err != nil {
		t.Fatal(err)
	}
	altOut := filepath.Join(projectDir, "out")

	res, err := Build(context.Background(), Options{
		SrcDir: srcDir, DistDir: altOut, ManifestPath: altManifest, Now: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(altOut, "test-driver.c4z")
	if res.C4zPath != want {
		t.Errorf("C4zPath = %q, want %q", res.C4zPath, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Errorf("artifact not written to the custom output dir: %v", err)
	}
}
