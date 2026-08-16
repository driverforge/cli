package driverxml

import (
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
)

// Property is a single configurable driver property declared in driver.xml's
// <config><properties>…</properties></config> block. These are the settings an
// installer fills in when the driver is added to a project; on a first deploy we
// render them as a form so the values can be sent to the controller alongside
// the install.
//
// The element shape we read is, e.g.:
//
//	<property>
//	  <name>Mode</name>
//	  <type>LIST</type>
//	  <default>Auto</default>
//	  <items><item>Auto</item><item>Manual</item></items>
//	</property>
//
// Only the fields driverforge needs to render a form are surfaced; Control4 carries
// additional, type-specific elements we deliberately ignore.
type Property struct {
	Name    string   // display name / key, e.g. "Mode"
	Type    string   // upper-cased Control4 type, e.g. "LIST", "STRING", "BOOL"
	Default string   // declared default value ("" if none)
	Items   []string // allowed values for LIST / DYNAMIC_LIST types
	Min     *int     // lower bound for RANGED_INTEGER (nil if unspecified)
	Max     *int     // upper bound for RANGED_INTEGER (nil if unspecified)
	// ReadOnly marks a property the installer cannot edit (Control4 surfaces it
	// for display only). Such properties are shown but never collected.
	ReadOnly bool
	// Password marks a value that should be masked in the form. Independent of
	// Type so a STRING can opt into masking via <password>true</property>.
	Password bool
}

// Control4 property type tokens. The first-deploy interview prompts for the
// interactive ones (a value the installer sets at deploy) and skips the rest —
// display-only, populated by the driver at runtime, or needing a picker the
// interview can't offer yet.
const (
	// Interactive — prompted in the first-deploy interview.
	TypeString        = "STRING"
	TypeBool          = "BOOL"
	TypeList          = "LIST"
	TypeInteger       = "INTEGER"
	TypeRangedInteger = "RANGED_INTEGER"
	TypePassword      = "PASSWORD"
	// TypeRangedFloat / TypeScroll / TypeTrack are integer/float controls in
	// Composer; the stored value is a plain number. Their exact tokens are
	// inferred from the Control4 type list (none appear in sampled drivers) — a
	// wrong guess only means the property is skipped, never mis-collected.
	TypeRangedFloat = "RANGED_FLOAT"
	TypeScroll      = "SCROLL"
	TypeTrack       = "TRACK"

	// Non-interactive — skipped. TypeLabel/TypeLink are display-only;
	// TypeDynamicList is populated by the driver at runtime; TypeDeviceSelector /
	// TypeColorSelector need a picker the terminal interview doesn't offer yet.
	// Director applies the driver.xml default for any property the install
	// doesn't send.
	TypeLabel          = "LABEL"
	TypeDynamicList    = "DYNAMIC_LIST"
	TypeLink           = "LINK"
	TypeDeviceSelector = "DEVICE_SELECTOR"
	TypeColorSelector  = "COLOR_SELECTOR"
)

// xmlProperty mirrors the on-disk <property> element. We keep raw string fields
// for the booleans (Control4 writes "true"/"True"/"1") and parse them in toProperty.
type xmlProperty struct {
	Name     string   `xml:"name"`
	Type     string   `xml:"type"`
	Default  string   `xml:"default"`
	Items    []string `xml:"items>item"`
	Minimum  *int     `xml:"minimum"`
	Maximum  *int     `xml:"maximum"`
	ReadOnly string   `xml:"readonly"`
	Password string   `xml:"password"`
}

// xmlPropertiesDoc anchors the decoder to devicedata>config>properties>property.
// Anything outside that path (actions, capabilities, the driver script) is
// ignored by encoding/xml.
type xmlPropertiesDoc struct {
	XMLName    xml.Name      `xml:"devicedata"`
	Properties []xmlProperty `xml:"config>properties>property"`
}

// Properties parses the configurable properties from a driver.xml document, in
// document order. A driver with no <properties> block yields an empty slice and
// a nil error — having no properties is normal, not a failure. A malformed
// document returns the decoder error.
func Properties(src []byte) ([]Property, error) {
	var doc xmlPropertiesDoc
	if err := xml.Unmarshal(src, &doc); err != nil {
		return nil, fmt.Errorf("driver.xml: parsing properties: %w", err)
	}
	out := make([]Property, 0, len(doc.Properties))
	for _, p := range doc.Properties {
		out = append(out, toProperty(p))
	}
	return out, nil
}

func toProperty(p xmlProperty) Property {
	prop := Property{
		Name:     strings.TrimSpace(p.Name),
		Type:     strings.ToUpper(strings.TrimSpace(p.Type)),
		Default:  p.Default,
		Min:      p.Minimum,
		Max:      p.Maximum,
		ReadOnly: parseXMLBool(p.ReadOnly),
		Password: parseXMLBool(p.Password),
	}
	for _, it := range p.Items {
		if it = strings.TrimSpace(it); it != "" {
			prop.Items = append(prop.Items, it)
		}
	}
	// PASSWORD-typed properties are implicitly masked.
	if prop.Type == TypePassword {
		prop.Password = true
	}
	return prop
}

// parseXMLBool accepts the several truthy spellings Control4 emits.
func parseXMLBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "yes":
		return true
	default:
		if b, err := strconv.ParseBool(strings.TrimSpace(s)); err == nil {
			return b
		}
		return false
	}
}
