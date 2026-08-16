package manifest

import "testing"

func TestParseBytes(t *testing.T) {
	d, err := ParseBytes([]byte(`<Driver type="c4z" name="acme" squishLua="true">
  <PrepackageCommands><PrepackageCommand>echo hi</PrepackageCommand></PrepackageCommands>
  <Items>
    <Item type="dir" c4zDir="www" name="www" recurse="true"/>
    <Item type="dir" name="vendor"/>
    <Item type="file" name="driver.lua"/>
    <Item type="file" name="skip.lua" exclude="true"/>
  </Items>
</Driver>`), "test")
	if err != nil {
		t.Fatal(err)
	}
	if d.Type != "c4z" || d.Name != "acme" || !d.SquishLua {
		t.Fatalf("driver attrs wrong: %+v", d)
	}
	if len(d.Pre) != 1 || d.Pre[0] != "echo hi" {
		t.Errorf("Pre = %v", d.Pre)
	}
	if len(d.Items) != 4 {
		t.Fatalf("got %d items, want 4", len(d.Items))
	}
	if !d.Items[0].Recurse {
		t.Error("www should have recurse=true")
	}
	if d.Items[1].Recurse {
		t.Error("vendor should default recurse=false")
	}
	if !d.Items[3].Exclude {
		t.Error("skip.lua should be exclude=true")
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string]string{
		"wrong root":   `<Nope/>`,
		"missing type": `<Driver name="x"><Items/></Driver>`,
		"missing name": `<Driver type="c4z"><Items/></Driver>`,
		"item no type": `<Driver type="c4z" name="x"><Items><Item name="a"/></Items></Driver>`,
	}
	for desc, xml := range cases {
		if _, err := ParseBytes([]byte(xml), "t"); err == nil {
			t.Errorf("%s: expected error", desc)
		}
	}
}

// Boolean attributes accept the common spellings (case-insensitively), not just
// the exact "true" — "True" must not silently drop files from a recursive dir.
func TestParseBoolSpellings(t *testing.T) {
	truthy := []string{"true", "True", "TRUE", "1", "yes", "Y", "t"}
	for _, v := range truthy {
		d, err := ParseBytes([]byte(`<Driver type="c4z" name="x" squishLua="`+v+`"><Items>`+
			`<Item type="dir" name="src" recurse="`+v+`"/></Items></Driver>`), "t")
		if err != nil {
			t.Fatalf("%q: %v", v, err)
		}
		if !d.SquishLua {
			t.Errorf("squishLua=%q parsed as false", v)
		}
		if !d.Items[0].Recurse {
			t.Errorf("recurse=%q parsed as false", v)
		}
	}
	falsy := []string{"", "false", "False", "0", "no", "n", "f"}
	for _, v := range falsy {
		d, err := ParseBytes([]byte(`<Driver type="c4z" name="x" squishLua="`+v+`"><Items>`+
			`<Item type="dir" name="src" recurse="`+v+`"/></Items></Driver>`), "t")
		if err != nil {
			t.Fatalf("%q: %v", v, err)
		}
		if d.SquishLua || d.Items[0].Recurse {
			t.Errorf("%q parsed as true", v)
		}
	}
}

// A non-boolean value (a typo) is rejected loudly rather than silently
// defaulting to false and dropping files.
func TestParseBoolRejectsGarbage(t *testing.T) {
	cases := []string{
		`<Driver type="c4z" name="x" squishLua="ture"><Items/></Driver>`,
		`<Driver type="c4z" name="x"><Items><Item type="dir" name="s" recurse="maybe"/></Items></Driver>`,
		`<Driver type="c4z" name="x"><Items><Item type="file" name="s" exclude="nope"/></Items></Driver>`,
	}
	for _, xml := range cases {
		if _, err := ParseBytes([]byte(xml), "t"); err == nil {
			t.Errorf("expected a boolean-parse error for %q", xml)
		}
	}
}
