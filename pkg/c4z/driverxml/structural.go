package driverxml

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
)

// structuralMetadata are the top-level <devicedata> children excluded from the
// structural hash: cosmetic/identity fields whose change does NOT require a
// driver reload. Only direct children (2-space indent) are stripped — nested
// elements like <action><name> are deliberately retained so their *values* are
// part of the hash.
var structuralMetadata = []string{
	"copyright", "manufacturer", "name", "model",
	"modified", "version", "created",
	"small", "large", "documentation", "creator", "author",
}

var (
	reInterTagWS  = regexp.MustCompile(`>\s+<`)
	rePropDefault = regexp.MustCompile(`(?s)<default\b[^>]*>.*?</default>|<default\b[^>]*/>`)
)

// StructuralHash returns "sha256:<hex>" over a canonical form of driver.xml that
// captures only interface-affecting content — actions, properties, proxies,
// capabilities, connections, the <script encryption> flag, control/controlmethod
// — *including their values*, while excluding metadata (version, modified,
// manufacturer, display name, model, icons, …) and property default values, and
// being insensitive to the ordering of <action>/<property> entries and to
// insignificant whitespace.
//
// So renaming an action or changing a command flips the hash (full deploy
// needed), while a pure-Lua change or a version/metadata bump does not (a hot
// `sync` is safe). The agent treats the value as opaque; this is the agreed
// algorithm (handover §6).
func StructuralHash(driverXML []byte) (string, error) {
	s := string(driverXML)
	for _, el := range structuralMetadata {
		s = stripTopLevelElement(s, el)
	}
	s = rePropDefault.ReplaceAllString(s, "")
	s = sortChildBlocks(s, "actions", "action")
	s = sortChildBlocks(s, "properties", "property")
	s = reInterTagWS.ReplaceAllString(s, "><")
	s = strings.TrimSpace(s)
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// stripTopLevelElement removes a direct child of <devicedata> (2-space indent),
// paired or self-closing, including multiline bodies (e.g. base64 icons).
func stripTopLevelElement(s, el string) string {
	paired := regexp.MustCompile(`(?ms)^  <` + el + `\b[^>]*>.*?</` + el + `>[ \t]*\n?`)
	s = paired.ReplaceAllString(s, "")
	selfClose := regexp.MustCompile(`(?m)^  <` + el + `\b[^>]*/>[ \t]*\n?`)
	return selfClose.ReplaceAllString(s, "")
}

// sortChildBlocks canonicalises the order of <item> entries within <container>
// by sorting their full text, so reordering them does not change the hash.
func sortChildBlocks(s, container, item string) string {
	containerRe := regexp.MustCompile(`(?s)<` + container + `\b[^>]*>(.*?)</` + container + `>`)
	itemRe := regexp.MustCompile(`(?s)<` + item + `\b[^>]*>.*?</` + item + `>`)
	return containerRe.ReplaceAllStringFunc(s, func(block string) string {
		inner := containerRe.FindStringSubmatch(block)[1]
		items := itemRe.FindAllString(inner, -1)
		sort.Strings(items)
		return "<" + container + ">" + strings.Join(items, "") + "</" + container + ">"
	})
}
