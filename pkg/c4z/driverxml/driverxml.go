// Package driverxml applies the build-time edits to a driver.xml document in
// memory, producing the bytes that get written into the .c4z (the Control4
// toolchain likewise injected a modified driver.xml from a buffer rather than
// mutating the source on disk).
//
// The integer <version> is the only version driverforge knows about: WriteVersion
// stamps a caller-supplied value; nothing is derived. Non-standard elements pass
// through untouched, like any other content driverforge doesn't manage.
//
// The edits are deliberately text-based so the result matches the Control4
// toolchain's output byte-for-byte: that chain edited the same elements with sed and then
// round-tripped through lxml, which preserved all other formatting and stripped
// the trailing newline. We reproduce exactly that: set <modified>, optionally
// suffix the top-level device <name>, and trim trailing whitespace.
package driverxml

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// Edits are the values applied to a driver.xml.
type Edits struct {
	NameSuffix string    // appended to the device <name> ("" => unchanged)
	Modified   time.Time // written to <modified>
	// Encrypt overrides the driver's script encryption for this build only
	// (the source is never modified): nil leaves driver.xml as authored, *true
	// adds encryption="2" to the main <script>, *false strips it.
	Encrypt *bool
}

// EncryptionInfo reports whether the driver requests script encryption.
type EncryptionInfo struct {
	Enabled bool
	File    string // the <script file="..."> referenced for encryption
}

// modifiedLayout matches the Control4 Python tool's format "%m/%d/%Y %I:%M %p".
const modifiedLayout = "01/02/2006 03:04 PM"

// ErrNameNotFound is returned when a build-configuration name suffix was
// requested but driver.xml has no recognisable top-level <name> element — the
// configuration build would otherwise be indistinguishable from the default
// in Composer.
var ErrNameNotFound = errors.New(`driver.xml: top-level <name> element not found (needed to suffix the build-configuration name)`)

var (
	reVersion  = regexp.MustCompile(`<version>([^<]*)</version>`)
	reModified = regexp.MustCompile(`<modified>[^<]*</modified>`)
	// reName matches the top-level device <name> — indented by one level (a
	// tab, or 2-4 spaces) — never the deeper-nested <action><name> elements.
	reName     = regexp.MustCompile(`(?m)^((?:\t| {2,4})<name>)([^<]*)(</name>)`)
	reScript2  = regexp.MustCompile(`<script\b[^>]*\bencryption="2"[^>]*>`)
	reFileAttr = regexp.MustCompile(`\bfile="([^"]*)"`)
	// reScriptFile matches a <script ... file="..."> tag — the encryptable
	// driver script that --encrypt toggles.
	reScriptFile = regexp.MustCompile(`<script\b[^>]*\bfile="[^"]*"[^>]*>`)
	// reEncAttr matches the encryption="2" attribute (with its leading space)
	// for stripping.
	reEncAttr = regexp.MustCompile(`\s+encryption="2"`)
	// reComment matches XML comments, so element searches can skip content
	// that is commented out (a hand-edited driver.xml often carries e.g.
	// `<!-- <version>7</version> -->` above the live element).
	reComment = regexp.MustCompile(`(?s)<!--.*?-->`)
)

// findOutsideComments returns re's first match (as a FindIndex pair) that does
// not start inside an XML comment, or nil.
func findOutsideComments(re *regexp.Regexp, src []byte) []int {
	comments := reComment.FindAllIndex(src, -1)
	for _, m := range re.FindAllIndex(src, -1) {
		if !insideAny(m[0], comments) {
			return m
		}
	}
	return nil
}

// findSubmatchOutsideComments is findOutsideComments for submatch indices.
func findSubmatchOutsideComments(re *regexp.Regexp, src []byte) []int {
	comments := reComment.FindAllIndex(src, -1)
	for _, m := range re.FindAllSubmatchIndex(src, -1) {
		if !insideAny(m[0], comments) {
			return m
		}
	}
	return nil
}

func insideAny(pos int, ranges [][]int) bool {
	for _, r := range ranges {
		if pos >= r[0] && pos < r[1] {
			return true
		}
	}
	return false
}

// enableEncryption adds encryption="2" to the main <script file="..."> tag if
// the document does not already request encryption. It is a no-op when no such
// script exists or encryption is already enabled.
func enableEncryption(src []byte) []byte {
	if findOutsideComments(reScript2, src) != nil {
		return src // already enabled
	}
	loc := findOutsideComments(reScriptFile, src)
	if loc == nil {
		return src // no encryptable script to mark
	}
	at := loc[0] + len("<script")
	return splice(src, at, at, []byte(` encryption="2"`))
}

// disableEncryption removes any encryption="2" attribute so the build ships
// unencrypted, regardless of what the source driver.xml declares.
func disableEncryption(src []byte) []byte {
	return reEncAttr.ReplaceAll(src, nil)
}

// ScriptFile returns the file attribute of the driver's main
// <script file="..."> tag — the script Director executes. ok is false when
// driver.xml declares no such tag (or it carries no file attribute).
func ScriptFile(src []byte) (string, bool) {
	loc := findOutsideComments(reScriptFile, src)
	if loc == nil {
		return "", false
	}
	m := reFileAttr.FindSubmatch(src[loc[0]:loc[1]])
	if m == nil || len(m[1]) == 0 {
		return "", false
	}
	return string(m[1]), true
}

// ReadVersion extracts the integer <version> value from a driver.xml. ok is
// false if the element is absent.
func ReadVersion(src []byte) (version string, ok bool) {
	m := findSubmatchOutsideComments(reVersion, src)
	if m == nil {
		return "", false
	}
	return string(src[m[2]:m[3]]), true
}

// ReadName extracts the top-level device <name> from a driver.xml (the driver's
// display name, e.g. "Acme Thermostat"). ok is false if absent. It matches the
// one-indent-level element (tab or 2-4 spaces), never the deeper <action><name>.
func ReadName(src []byte) (name string, ok bool) {
	m := findSubmatchOutsideComments(reName, src)
	if m == nil {
		return "", false
	}
	return string(src[m[4]:m[5]]), true
}

// WriteVersion returns driver.xml bytes with the integer <version> value
// replaced by newVer, preserving all other formatting. It errors if no
// <version> element is present.
func WriteVersion(src []byte, newVer string) ([]byte, error) {
	loc := findOutsideComments(reVersion, src)
	if loc == nil {
		return nil, fmt.Errorf("driver.xml: <version> element not found")
	}
	repl := fmt.Sprintf("<version>%s</version>", newVer)
	return splice(src, loc[0], loc[1], []byte(repl)), nil
}

// Transform applies edits to src and returns the new document bytes plus the
// driver's encryption request. Encryption is taken from <config><script
// encryption="2">, after applying any e.Encrypt override.
func Transform(src []byte, e Edits) ([]byte, EncryptionInfo, error) {
	out := src

	// <version> is not Transform's concern: a bump/override is seeded into the
	// work copy before the transform runs (see c4z.Build), and an untouched
	// driver passes through exactly as authored.

	if loc := findOutsideComments(reModified, out); loc != nil {
		repl := fmt.Sprintf("<modified>%s</modified>", e.Modified.Format(modifiedLayout))
		out = splice(out, loc[0], loc[1], []byte(repl))
	}

	if e.NameSuffix != "" {
		m := findSubmatchOutsideComments(reName, out)
		if m == nil {
			// Silently shipping a configuration build indistinguishable from
			// the default in Composer is worse than failing loudly.
			return nil, EncryptionInfo{}, ErrNameNotFound
		}
		var b bytes.Buffer
		b.Write(out[:m[2]])     // up to start of group 1
		b.Write(out[m[2]:m[3]]) // "<name>" with its indent
		b.Write(out[m[4]:m[5]]) // existing name text
		b.WriteString(e.NameSuffix)
		b.Write(out[m[6]:m[7]]) // "</name>"
		b.Write(out[m[1]:])     // remainder after the full match
		out = b.Bytes()
	}

	// Apply a build-time encryption override before probing the final state.
	if e.Encrypt != nil {
		if *e.Encrypt {
			out = enableEncryption(out)
		} else {
			out = disableEncryption(out)
		}
	}

	enc := EncryptionInfo{}
	if loc := findOutsideComments(reScript2, out); loc != nil {
		enc.Enabled = true
		if fm := reFileAttr.FindSubmatch(out[loc[0]:loc[1]]); fm != nil {
			enc.File = string(fm[1])
		}
	}

	// lxml dropped trailing whitespace after the root element.
	out = bytes.TrimRight(out, " \t\r\n")
	return out, enc, nil
}

func splice(src []byte, start, end int, repl []byte) []byte {
	out := make([]byte, 0, len(src)-(end-start)+len(repl))
	out = append(out, src[:start]...)
	out = append(out, repl...)
	out = append(out, src[end:]...)
	return out
}
