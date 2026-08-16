// Package manifest parses Control4 driver manifest files (.c4zproj).
//
// A manifest describes how a driver's source tree is assembled into a .c4z
// archive: which directories and files to include, where they land inside the
// archive, and whether the Lua source is bundled ("squished"). It mirrors the
// subset of the Control4 Python driverpackager manifest format that Driverforge drivers
// use (driver type "c4z"; the "c4i" type is intentionally unsupported).
package manifest

import (
	"encoding/xml"
	"fmt"
	"os"
	"strings"
)

// ItemType is the kind of a manifest <Item>: a directory or a single file.
type ItemType string

const (
	ItemDir  ItemType = "dir"
	ItemFile ItemType = "file"
)

// Item is a single entry in the manifest's <Items> list.
type Item struct {
	Type    ItemType
	Name    string // path relative to the source directory
	C4zDir  string // destination directory inside the .c4z ("" => use Name)
	Recurse bool   // for dirs: descend into subdirectories
	Exclude bool   // skip this item entirely
}

// Driver is a parsed manifest.
type Driver struct {
	Type      string // "c4z" (the only supported type)
	Name      string // drives the output artifact name
	SquishLua bool   // bundle Lua source via the squishy file
	Items     []Item
	Pre       []string // PrepackageCommands (shell strings)
	Post      []string // PostpackageCommands (shell strings)
}

type xmlItem struct {
	Type    string `xml:"type,attr"`
	Name    string `xml:"name,attr"`
	C4zDir  string `xml:"c4zDir,attr"`
	Recurse string `xml:"recurse,attr"`
	Exclude string `xml:"exclude,attr"`
}

type xmlDriver struct {
	XMLName   xml.Name  `xml:"Driver"`
	Type      string    `xml:"type,attr"`
	Name      string    `xml:"name,attr"`
	SquishLua string    `xml:"squishLua,attr"`
	Pre       []string  `xml:"PrepackageCommands>PrepackageCommand"`
	Post      []string  `xml:"PostpackageCommands>PostpackageCommand"`
	Items     []xmlItem `xml:"Items>Item"`
}

// Parse reads and parses a manifest (.c4zproj) file.
func Parse(path string) (*Driver, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	return ParseBytes(data, path)
}

// ParseBytes parses manifest XML. name is used only for error messages.
func ParseBytes(data []byte, name string) (*Driver, error) {
	var xd xmlDriver
	if err := xml.Unmarshal(data, &xd); err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", name, err)
	}
	if xd.XMLName.Local != "Driver" {
		return nil, fmt.Errorf("manifest %s: root element must be <Driver>, got <%s>", name, xd.XMLName.Local)
	}
	if xd.Type == "" {
		return nil, fmt.Errorf("manifest %s: missing Driver 'type' attribute", name)
	}
	if xd.Name == "" {
		return nil, fmt.Errorf("manifest %s: missing Driver 'name' attribute", name)
	}

	squish, err := boolAttr(name, "squishLua", xd.SquishLua)
	if err != nil {
		return nil, err
	}
	d := &Driver{
		Type:      xd.Type,
		Name:      xd.Name,
		SquishLua: squish,
		Pre:       trimAll(xd.Pre),
		Post:      trimAll(xd.Post),
	}
	for _, xi := range xd.Items {
		if xi.Type == "" {
			return nil, fmt.Errorf("manifest %s: <Item> missing 'type' attribute", name)
		}
		if xi.Name == "" {
			return nil, fmt.Errorf("manifest %s: <Item> missing 'name' attribute", name)
		}
		recurse, err := boolAttr(name, "recurse", xi.Recurse)
		if err != nil {
			return nil, err
		}
		exclude, err := boolAttr(name, "exclude", xi.Exclude)
		if err != nil {
			return nil, err
		}
		d.Items = append(d.Items, Item{
			Type:    ItemType(xi.Type),
			Name:    xi.Name,
			C4zDir:  xi.C4zDir,
			Recurse: recurse,
			Exclude: exclude,
		})
	}
	return d, nil
}

// boolAttr parses a manifest boolean attribute, accepting the common truthy and
// falsy spellings (case-insensitive). An absent attribute defaults to false; an
// unrecognised value is rejected — a typo like recurse="ture" would otherwise
// silently default to false and drop files from the archive.
func boolAttr(manifestName, attr, value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return false, nil
	case "true", "1", "yes", "y", "t":
		return true, nil
	case "false", "0", "no", "n", "f":
		return false, nil
	default:
		return false, fmt.Errorf("manifest %s: %s=%q is not a boolean (use true or false)", manifestName, attr, value)
	}
}

func trimAll(in []string) []string {
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
