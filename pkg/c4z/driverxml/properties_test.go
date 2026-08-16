package driverxml

import "testing"

const propsXML = `<devicedata>
  <name>Test Driver</name>
  <version>000001</version>
  <config>
    <properties>
      <property>
        <name>Host</name>
        <type>STRING</type>
        <default>192.168.1.10</default>
      </property>
      <property>
        <name>Mode</name>
        <type>LIST</type>
        <default>Auto</default>
        <items>
          <item>Auto</item>
          <item>Manual</item>
        </items>
      </property>
      <property>
        <name>Enabled</name>
        <type>BOOL</type>
        <default>True</default>
      </property>
      <property>
        <name>Port</name>
        <type>RANGED_INTEGER</type>
        <default>80</default>
        <minimum>1</minimum>
        <maximum>65535</maximum>
      </property>
      <property>
        <name>API Key</name>
        <type>STRING</type>
        <default></default>
        <password>true</password>
      </property>
      <property>
        <name>Firmware</name>
        <type>STRING</type>
        <default>1.0</default>
        <readonly>true</readonly>
      </property>
    </properties>
  </config>
</devicedata>`

func TestPropertiesParsesAllFields(t *testing.T) {
	props, err := Properties([]byte(propsXML))
	if err != nil {
		t.Fatalf("Properties: %v", err)
	}
	if len(props) != 6 {
		t.Fatalf("got %d properties, want 6", len(props))
	}

	// Document order is preserved.
	if props[0].Name != "Host" || props[1].Name != "Mode" {
		t.Errorf("order not preserved: %q, %q", props[0].Name, props[1].Name)
	}

	host := props[0]
	if host.Type != TypeString || host.Default != "192.168.1.10" {
		t.Errorf("Host = %+v", host)
	}

	mode := props[1]
	if mode.Type != TypeList || mode.Default != "Auto" {
		t.Errorf("Mode = %+v", mode)
	}
	if len(mode.Items) != 2 || mode.Items[0] != "Auto" || mode.Items[1] != "Manual" {
		t.Errorf("Mode items = %v, want [Auto Manual]", mode.Items)
	}

	if props[2].Type != TypeBool || !parseXMLBool(props[2].Default) {
		t.Errorf("Enabled = %+v (default should be truthy)", props[2])
	}

	port := props[3]
	if port.Type != TypeRangedInteger || port.Min == nil || *port.Min != 1 || port.Max == nil || *port.Max != 65535 {
		t.Errorf("Port = %+v (min/max not parsed)", port)
	}

	apiKey := props[4]
	if !apiKey.Password {
		t.Errorf("API Key should be masked via <password>true</password>: %+v", apiKey)
	}

	fw := props[5]
	if !fw.ReadOnly {
		t.Errorf("Firmware should be read-only: %+v", fw)
	}
}

func TestPropertiesPasswordTypeImpliesMasking(t *testing.T) {
	xml := `<devicedata><config><properties>
		<property><name>Secret</name><type>PASSWORD</type><default></default></property>
	</properties></config></devicedata>`
	props, err := Properties([]byte(xml))
	if err != nil {
		t.Fatalf("Properties: %v", err)
	}
	if len(props) != 1 || !props[0].Password {
		t.Errorf("a PASSWORD-typed property must be masked: %+v", props)
	}
}

func TestPropertiesEmptyIsNotAnError(t *testing.T) {
	// A driver with no <properties> block is normal.
	xml := `<devicedata><name>X</name><config><script file="driver.lua"/></config></devicedata>`
	props, err := Properties([]byte(xml))
	if err != nil {
		t.Fatalf("no-properties driver should parse cleanly: %v", err)
	}
	if len(props) != 0 {
		t.Errorf("want no properties, got %v", props)
	}
}

func TestReadName(t *testing.T) {
	src := []byte("<devicedata>\n  <name>Acme Thermostat</name>\n  <config>\n    <actions>\n      <action><name>Inner</name></action>\n    </actions>\n  </config>\n</devicedata>")
	got, ok := ReadName(src)
	if !ok || got != "Acme Thermostat" {
		t.Errorf("ReadName = %q, %v; want top-level name, not the <action><name>", got, ok)
	}
	if _, ok := ReadName([]byte("<devicedata></devicedata>")); ok {
		t.Error("absent <name> should report ok=false")
	}
}

func TestPropertiesMalformedXML(t *testing.T) {
	if _, err := Properties([]byte(`<devicedata><config><properties`)); err == nil {
		t.Fatal("malformed XML should return an error")
	}
}

func TestPropertiesTrimsAndUppercasesType(t *testing.T) {
	xml := `<devicedata><config><properties>
		<property><name>  Spaced  </name><type> list </type><default>a</default><items><item> a </item><item></item></items></property>
	</properties></config></devicedata>`
	props, err := Properties([]byte(xml))
	if err != nil {
		t.Fatalf("Properties: %v", err)
	}
	p := props[0]
	if p.Name != "Spaced" {
		t.Errorf("name not trimmed: %q", p.Name)
	}
	if p.Type != TypeList {
		t.Errorf("type not normalised: %q", p.Type)
	}
	if len(p.Items) != 1 || p.Items[0] != "a" {
		t.Errorf("items should trim and drop blanks: %v", p.Items)
	}
}
