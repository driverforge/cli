package driverxml

import (
	"strings"
	"testing"
	"time"
)

// sample carries a non-standard <vendorext> element: content driverforge doesn't
// manage must pass through Transform untouched.
const sample = `<devicedata>
  <name>Acme Driver</name>
  <modified>1/1/1970 00:00:00 AM</modified>
  <version>000001</version>
  <vendorext>0.0.0</vendorext>
  <config>
    <script jit="1" file="driver.lua"/>
    <actions>
      <action>
        <name>Do Thing</name>
      </action>
    </actions>
  </config>
</devicedata>
`

func transform(t *testing.T, src string, e Edits) (string, EncryptionInfo) {
	t.Helper()
	out, enc, err := Transform([]byte(src), e)
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	return string(out), enc
}

func TestTransformDev(t *testing.T) {
	now := time.Date(2026, 6, 9, 14, 5, 0, 0, time.UTC)
	out, _ := transform(t, sample, Edits{NameSuffix: " (dev)", Modified: now})

	for _, want := range []string{
		"<version>000001</version>",    // exactly as authored — Transform never versions
		"<vendorext>0.0.0</vendorext>", // unmanaged element passes through untouched
		"<modified>06/09/2026 02:05 PM</modified>",
		"  <name>Acme Driver (dev)</name>",
		"<name>Do Thing</name>", // action name must be untouched
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "Do Thing (dev)") {
		t.Error("action <name> was incorrectly suffixed")
	}
	if strings.HasSuffix(out, "\n") {
		t.Error("trailing newline should be trimmed (lxml parity)")
	}
}

func TestTransformProdNoSuffix(t *testing.T) {
	out, _ := transform(t, sample, Edits{NameSuffix: "", Modified: time.Now()})
	if !strings.Contains(out, "  <name>Acme Driver</name>") {
		t.Errorf("prod device name must be unchanged:\n%s", out)
	}
}

func TestEncryptionProbe(t *testing.T) {
	enc := func(xml string) EncryptionInfo {
		_, e, err := Transform([]byte(xml), Edits{Modified: time.Now()})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}

	if got := enc(sample); got.Enabled {
		t.Errorf("expected encryption disabled, got %+v", got)
	}

	encXML := strings.Replace(sample,
		`<script jit="1" file="driver.lua"/>`,
		`<script jit="1" encryption="2" file="driver.lua"/>`, 1)
	if got := enc(encXML); !got.Enabled || got.File != "driver.lua" {
		t.Errorf("expected enabled with file=driver.lua, got %+v", got)
	}
}

func TestTransformEncryptForceOn(t *testing.T) {
	on := true
	out, enc := transform(t, sample, Edits{Modified: time.Now(), Encrypt: &on})
	if !enc.Enabled || enc.File != "driver.lua" {
		t.Errorf("encryption not enabled: %+v", enc)
	}
	if !strings.Contains(out, `encryption="2"`) {
		t.Errorf("encryption=\"2\" not injected into driver.xml:\n%s", out)
	}
	if n := strings.Count(out, `encryption="2"`); n != 1 {
		t.Errorf("expected exactly one encryption attribute, got %d", n)
	}
}

func TestTransformEncryptForceOff(t *testing.T) {
	encXML := strings.Replace(sample,
		`<script jit="1" file="driver.lua"/>`,
		`<script jit="1" encryption="2" file="driver.lua"/>`, 1)
	off := false
	out, enc := transform(t, encXML, Edits{Modified: time.Now(), Encrypt: &off})
	if enc.Enabled {
		t.Errorf("encryption should be disabled: %+v", enc)
	}
	if strings.Contains(out, `encryption="2"`) {
		t.Errorf("encryption=\"2\" should have been stripped:\n%s", out)
	}
}

func TestTransformEncryptNilLeavesAsIs(t *testing.T) {
	// nil override: a driver requesting encryption keeps it.
	encXML := strings.Replace(sample,
		`<script jit="1" file="driver.lua"/>`,
		`<script jit="1" encryption="2" file="driver.lua"/>`, 1)
	_, enc := transform(t, encXML, Edits{Modified: time.Now(), Encrypt: nil})
	if !enc.Enabled {
		t.Error("nil override must leave the driver's encryption request intact")
	}
	// nil override: a plain driver stays plain.
	_, enc2 := transform(t, sample, Edits{Modified: time.Now(), Encrypt: nil})
	if enc2.Enabled {
		t.Error("nil override must not enable encryption on a plain driver")
	}
}

// Transform has no versioning concerns at all: a driver without <version> (or
// without <modified>) transforms without error, everything as authored.
func TestTransformNoVersionElement(t *testing.T) {
	src := "<devicedata>\n  <name>Acme Driver</name>\n</devicedata>\n"
	out, _ := transform(t, src, Edits{Modified: time.Now()})
	if strings.Contains(out, "<version>") || strings.Contains(out, "<modified>") {
		t.Errorf("nothing should be injected into a minimal driver.xml:\n%s", out)
	}
}

func TestReadVersion(t *testing.T) {
	if v, ok := ReadVersion([]byte(sample)); !ok || v != "000001" {
		t.Errorf("ReadVersion = (%q, %v), want (000001, true)", v, ok)
	}
	if _, ok := ReadVersion([]byte("<devicedata></devicedata>")); ok {
		t.Error("ReadVersion should report ok=false when <version> is absent")
	}
}

func TestWriteVersion(t *testing.T) {
	out, err := WriteVersion([]byte(sample), "000099")
	if err != nil {
		t.Fatalf("WriteVersion: %v", err)
	}
	if v, _ := ReadVersion(out); v != "000099" {
		t.Errorf("after WriteVersion, version = %q, want 000099", v)
	}
	if _, err := WriteVersion([]byte("<devicedata></devicedata>"), "1"); err == nil {
		t.Error("expected error writing version when element is absent")
	}
}

func TestScriptFile(t *testing.T) {
	if f, ok := ScriptFile([]byte(sample)); !ok || f != "driver.lua" {
		t.Errorf("ScriptFile = (%q, %v), want (driver.lua, true)", f, ok)
	}
	if _, ok := ScriptFile([]byte("<devicedata></devicedata>")); ok {
		t.Error("ScriptFile should report ok=false when no script tag exists")
	}
}

// A hand-edited driver.xml often carries commented-out old values above the
// live elements. Reads and writes must target the live element, never the
// comment.
func TestCommentedOutElementsAreSkipped(t *testing.T) {
	src := `<devicedata>
  <name>Acme Driver</name>
  <!-- <version>7</version> old release -->
  <!-- <modified>01/01/2020 09:00 AM</modified> -->
  <modified>1/1/1970 00:00:00 AM</modified>
  <version>000042</version>
  <config>
    <!-- <script jit="1" encryption="2" file="old.lua"/> -->
    <script jit="1" file="driver.lua"/>
  </config>
</devicedata>
`
	if v, ok := ReadVersion([]byte(src)); !ok || v != "000042" {
		t.Errorf("ReadVersion = (%q, %v), want the live 000042", v, ok)
	}

	out, err := WriteVersion([]byte(src), "000043")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "<version>000043</version>") {
		t.Errorf("live <version> not updated:\n%s", out)
	}
	if !strings.Contains(string(out), "<!-- <version>7</version> old release -->") {
		t.Errorf("comment must be untouched:\n%s", out)
	}

	now := time.Date(2026, 6, 9, 14, 5, 0, 0, time.UTC)
	tOut, enc := transform(t, src, Edits{Modified: now})
	if !strings.Contains(tOut, "<modified>06/09/2026 02:05 PM</modified>") {
		t.Errorf("live <modified> not stamped:\n%s", tOut)
	}
	if !strings.Contains(tOut, "<!-- <modified>01/01/2020 09:00 AM</modified> -->") {
		t.Errorf("commented <modified> must be untouched:\n%s", tOut)
	}
	// The commented-out encrypted script must not enable encryption.
	if enc.Enabled {
		t.Error("a commented-out encryption=\"2\" script must not enable encryption")
	}
	if f, _ := ScriptFile([]byte(src)); f != "driver.lua" {
		t.Errorf("ScriptFile = %q, want the live driver.lua", f)
	}
}

// --encrypt must mark the live script tag, not one inside a comment.
func TestEnableEncryptionSkipsComments(t *testing.T) {
	src := `<devicedata>
  <config>
    <!-- <script jit="1" file="legacy.lua"/> -->
    <script jit="1" file="driver.lua"/>
  </config>
</devicedata>
`
	on := true
	out, enc := transform(t, src, Edits{Modified: time.Now(), Encrypt: &on})
	if !enc.Enabled || enc.File != "driver.lua" {
		t.Errorf("encryption should target the live script: %+v", enc)
	}
	if strings.Contains(out, `<!-- <script jit="1" encryption="2"`) {
		t.Errorf("the commented script must not be marked:\n%s", out)
	}
}

// The device <name> is recognised at any single indent level — tab or 2-4
// spaces — so non-2-space files still get the configuration suffix.
func TestNameSuffixIndentVariants(t *testing.T) {
	cases := []struct{ name, indent string }{
		{"two spaces", "  "},
		{"four spaces", "    "},
		{"tab", "\t"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := "<devicedata>\n" + tc.indent + "<name>Acme Driver</name>\n" +
				tc.indent + tc.indent + tc.indent + "<name>Nested Action</name>\n</devicedata>\n"
			out, _ := transform(t, src, Edits{NameSuffix: " (dev)", Modified: time.Now()})
			if !strings.Contains(out, "<name>Acme Driver (dev)</name>") {
				t.Errorf("top-level name not suffixed:\n%s", out)
			}
			if strings.Contains(out, "Nested Action (dev)") {
				t.Errorf("nested name must not be suffixed:\n%s", out)
			}
			if n, ok := ReadName([]byte(src)); !ok || n != "Acme Driver" {
				t.Errorf("ReadName = (%q, %v), want Acme Driver", n, ok)
			}
		})
	}
}

// A requested suffix with no recognisable top-level <name> fails loudly — a
// dev build silently identical to prod in Composer is the hazard.
func TestNameSuffixMissingNameErrors(t *testing.T) {
	src := "<devicedata>\n<name>Root Level No Indent</name>\n</devicedata>\n"
	_, _, err := Transform([]byte(src), Edits{NameSuffix: " (dev)", Modified: time.Now()})
	if err == nil || err != ErrNameNotFound {
		t.Fatalf("error = %v, want ErrNameNotFound", err)
	}
	// Without a suffix the same document transforms fine.
	if _, _, err := Transform([]byte(src), Edits{Modified: time.Now()}); err != nil {
		t.Fatalf("no-suffix transform should succeed: %v", err)
	}
}
