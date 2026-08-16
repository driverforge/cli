package driverxml

import (
	"strings"
	"testing"
)

const structuralSample = `<devicedata>
  <copyright>Copyright 2026</copyright>
  <manufacturer>Acme</manufacturer>
  <name>Display Name</name>
  <modified>1/1/1970 00:00:00 AM</modified>
  <version>000001</version>
  <config>
    <script jit="1" file="driver.lua"/>
    <actions>
      <action>
        <name>Alpha</name>
        <command>A</command>
      </action>
      <action>
        <name>Beta</name>
        <command>B</command>
      </action>
    </actions>
    <properties>
      <property>
        <name>Mode</name>
        <type>LIST</type>
        <default>Auto</default>
        <items>
          <item>Auto</item>
          <item>Manual</item>
        </items>
      </property>
    </properties>
  </config>
</devicedata>`

func hashOf(t *testing.T, s string) string {
	t.Helper()
	h, err := StructuralHash([]byte(s))
	if err != nil {
		t.Fatalf("StructuralHash: %v", err)
	}
	if !strings.HasPrefix(h, "sha256:") {
		t.Fatalf("hash missing prefix: %q", h)
	}
	return h
}

func TestStructuralHash_MetadataIgnored(t *testing.T) {
	base := hashOf(t, structuralSample)
	meta := strings.NewReplacer(
		"000001", "000002",
		"1/1/1970 00:00:00 AM", "2/2/2000 01:02:03 PM",
		"Acme", "Globex",
		"Display Name", "Renamed Display",
		"Copyright 2026", "Copyright 2030",
	).Replace(structuralSample)
	if hashOf(t, meta) != base {
		t.Error("metadata-only changes must not affect the structural hash")
	}
}

func TestStructuralHash_DefaultIgnored(t *testing.T) {
	base := hashOf(t, structuralSample)
	d := strings.Replace(structuralSample, "<default>Auto</default>", "<default>Manual</default>", 1)
	if hashOf(t, d) != base {
		t.Error("property default value change must not affect the hash")
	}
}

func TestStructuralHash_ActionReorderIgnored(t *testing.T) {
	base := hashOf(t, structuralSample)
	reordered := strings.Replace(structuralSample,
		`<action>
        <name>Alpha</name>
        <command>A</command>
      </action>
      <action>
        <name>Beta</name>
        <command>B</command>
      </action>`,
		`<action>
        <name>Beta</name>
        <command>B</command>
      </action>
      <action>
        <name>Alpha</name>
        <command>A</command>
      </action>`, 1)
	if reordered == structuralSample {
		t.Fatal("reorder replacement did not apply")
	}
	if hashOf(t, reordered) != base {
		t.Error("reordering actions must not affect the hash")
	}
}

func TestStructuralHash_StructuralChanges(t *testing.T) {
	base := hashOf(t, structuralSample)
	cases := map[string]string{
		"rename action":   strings.Replace(structuralSample, "<name>Alpha</name>", "<name>Alfa</name>", 1),
		"change command":  strings.Replace(structuralSample, "<command>A</command>", "<command>AA</command>", 1),
		"rename property": strings.Replace(structuralSample, "<name>Mode</name>", "<name>Setting</name>", 1),
		"reorder items":   strings.Replace(structuralSample, "<item>Auto</item>\n          <item>Manual</item>", "<item>Manual</item>\n          <item>Auto</item>", 1),
		"add action":      strings.Replace(structuralSample, "</actions>", "  <action><name>Gamma</name><command>G</command></action>\n    </actions>", 1),
		"enable encrypt":  strings.Replace(structuralSample, `<script jit="1" file="driver.lua"/>`, `<script jit="1" encryption="2" file="driver.lua"/>`, 1),
		// Only the known-cosmetic metadata elements are excluded from the hash.
		// Anything else driverforge doesn't manage is assumed interface-affecting.
		"add unmanaged element": strings.Replace(structuralSample, "<version>000001</version>", "<version>000001</version>\n  <vendorext>1.0.0</vendorext>", 1),
	}
	for name, x := range cases {
		if x == structuralSample {
			t.Fatalf("%s: replacement did not apply", name)
		}
		if hashOf(t, x) == base {
			t.Errorf("%s: structural change must flip the hash", name)
		}
	}
}
