// Package initdriver computes the file changes that retrofit an existing
// Control4 driver for Driverforge: embedding the SDK bundle, wrapping the driver's
// OnDriverInit, registering the vendor dir in the manifest, and (optionally)
// scaffolding build-target config. The transforms here are pure (bytes in →
// bytes out) so they can be golden-tested; Plan wires them to the filesystem.
package initdriver

import (
	"bytes"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/driverforge/cli/internal/sdkrelease"
	"github.com/driverforge/cli/pkg/c4z/manifest"
)

// DefaultSDKDir is the source-relative directory used when init is not given
// an SDK destination.
const DefaultSDKDir = "./vendor"

// Decision captures everything the transforms need, resolved by the command
// layer from the user's choices.
type Decision struct {
	SDKBundle    []byte   // driverforge-sdk.lua contents to embed
	SDKVersion   string   // resolved SDK version (for messaging)
	ProjectToken string   // identifies the driver to the telemetry pipeline; not a credential
	BuildTargets bool     // true => scaffold per-build config (config.lua + config.<target>.lua + require('config'))
	Targets      []string // build-target names when BuildTargets (e.g. debug, release)
	LoggerGlobal string   // existing logger global to forward (e.g. "Log"); "" => none
	SDKDir       string   // source-relative directory for the embedded SDK; "" => vendor
}

// tokenArg renders the first argument to Driverforge:Init: the project token, which
// identifies the driver to the telemetry pipeline. It authorises nothing, does
// not expire and cannot be revoked, so it is always inlined and is identical
// across environments. Build targets exist for per-build config (service URLs,
// other vendors' credentials), not for hiding this.
func (d Decision) tokenArg() string {
	return strconv.Quote(d.ProjectToken)
}

// loggerOpt renders the optional logger option for Driverforge:Init.
func (d Decision) loggerOpt() string {
	if d.LoggerGlobal == "" {
		return ""
	}
	return ", { logger = " + d.LoggerGlobal + " }"
}

// VendorBundlePath is where init embeds the SDK bundle in a project's source
// tree. The planner writes it, `driverforge upgrade` checks it is still there,
// and the review UI hides it from the diff — so it gets one definition rather
// than three that can drift apart.
func VendorBundlePath(srcDir string, dirs ...string) string {
	return filepath.Join(srcDir, sdkDir(dirs...), sdkrelease.BundleName)
}

func sdkDir(dirs ...string) string {
	if len(dirs) == 0 || dirs[0] == "" {
		return filepath.Clean(DefaultSDKDir)
	}
	return filepath.Clean(dirs[0])
}

// ValidSDKDir reports whether dir is a non-empty path inside the driver's
// source directory.
func ValidSDKDir(dir string) bool {
	if dir == "" {
		return true
	}
	dir = filepath.Clean(dir)
	return filepath.IsLocal(dir)
}

func sdkRelPath(dirs ...string) string {
	return filepath.ToSlash(filepath.Join(sdkDir(dirs...), sdkrelease.BundleName))
}

func sdkModule(dirs ...string) string {
	dir := filepath.ToSlash(sdkDir(dirs...))
	if dir == "." {
		return "driverforge-sdk"
	}
	return strings.ReplaceAll(dir, "/", ".") + ".driverforge-sdk"
}

func sdkRequire(d Decision) string { return "require('" + sdkModule(d.SDKDir) + "')" }

// initCall renders the SDK entry point. The driver's filename is not passed:
// the SDK reads it from C4:GetDriverFileName() itself, so it always names the
// file actually running rather than a caller-supplied string that could
// mis-attribute usage.
func (d Decision) initCall() string {
	return "Driverforge:Init(" + d.tokenArg() + d.loggerOpt() + ")"
}

// alreadyPatched reports whether the entry file already wires in the SDK, so a
// re-run is a no-op rather than a double patch. It looks at code only (comments
// stripped): a commented-out `-- Driverforge:Init(...)` must not make init believe
// the driver is already wired and skip patching.
func alreadyPatched(src []byte) bool {
	s := string(stripComments(src))
	return strings.Contains(s, "driverforge-sdk") ||
		strings.Contains(s, "Driverforge:Init")
}

var onDriverInitRe = regexp.MustCompile(`function[ \t]+OnDriverInit[ \t]*\(([^)]*)\)`)

var configRequireRe = regexp.MustCompile(`require\(\s*['"]config['"]\s*\)`)

var sdkRequireRe = regexp.MustCompile(`require\s*\(\s*['"]([A-Za-z0-9_.-]+)['"]\s*\)`)

var driverforgeInitTokenRe = regexp.MustCompile(`Driverforge:Init\s*\(\s*("(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*')`)

// SDKDirFromEntry returns the source-relative SDK directory declared by a Lua
// require call, when one is present. A root-level SDK returns ".".
func SDKDirFromEntry(src []byte) (string, bool) {
	for _, match := range sdkRequireRe.FindAllSubmatch(stripComments(src), -1) {
		module := string(match[1])
		if module == "driverforge-sdk" {
			return ".", true
		}
		if !strings.HasSuffix(module, ".driverforge-sdk") {
			continue
		}
		dir := strings.TrimSuffix(module, ".driverforge-sdk")
		return filepath.FromSlash(strings.ReplaceAll(dir, ".", "/")), true
	}
	return "", false
}

// patchInitToken replaces the literal project token in an existing SDK init
// call. The offsets come from comment-stripped source, which preserves byte
// positions, so commented-out calls are never changed.
func patchInitToken(src []byte, d Decision) []byte {
	loc := driverforgeInitTokenRe.FindSubmatchIndex(stripComments(src))
	if loc == nil || string(src[loc[2]:loc[3]]) == d.tokenArg() {
		return src
	}
	out := make([]byte, 0, len(src)+len(d.tokenArg())-(loc[3]-loc[2]))
	out = append(out, src[:loc[2]]...)
	out = append(out, d.tokenArg()...)
	out = append(out, src[loc[3]:]...)
	return out
}

// PatchEntry rewrites the driver's entry Lua to wire in Driverforge. It returns the new
// bytes (equal to the input when already patched) and an optional warning when it
// could not wrap cleanly (no OnDriverInit, or an unbalanced body).
func PatchEntry(src []byte, d Decision) (out []byte, warning string) {
	if alreadyPatched(src) {
		return patchInitToken(src, d), ""
	}

	loc := onDriverInitRe.FindSubmatchIndex(src)
	if loc == nil {
		// No entry handler — add a fresh one at end of file.
		return ensureConfigRequire(append(append([]byte{}, src...), newOnDriverInit(d)...), d),
			"no existing OnDriverInit was found — added one; move your init code into it if needed"
	}

	headerStart, bodyStart := loc[0], loc[1]
	params := strings.TrimSpace(string(src[loc[2]:loc[3]]))

	endStart, endEnd, ok := matchingEnd(src, bodyStart)
	if !ok {
		// Couldn't balance the function — inject the wiring after the header and
		// leave a TODO rather than risk mangling the body.
		var b strings.Builder
		b.Write(src[:bodyStart])
		b.WriteString("\n    " + sdkRequire(d) + "\n")
		b.WriteString("    " + d.initCall() + "\n")
		b.WriteString("    -- TODO(driverforge): wrap the rest of this function's body in\n")
		b.WriteString("    -- Anvil:OnDriverInit(function(strDIR) ... end, strDIR) to defer it.\n")
		b.Write(src[bodyStart:])
		return ensureConfigRequire([]byte(b.String()), d),
			"could not parse OnDriverInit cleanly — added the SDK wiring with a TODO; review the diff carefully"
	}

	body := string(src[bodyStart:endStart])
	innerParam, passArg := wrapDirArgs(params)

	var b strings.Builder
	b.Write(src[:headerStart])
	b.WriteString("function OnDriverInit(" + params + ")\n")
	b.WriteString("    " + sdkRequire(d) + "\n")
	b.WriteString("    " + d.initCall() + "\n\n")
	b.WriteString("    Anvil:OnDriverInit(function(" + innerParam + ")\n")
	b.WriteString(reindent(body, "    "))
	b.WriteString("    end" + passArg + ")\n")
	b.WriteString("end")
	b.Write(src[endEnd:])

	return ensureConfigRequire([]byte(b.String()), d), ""
}

// wrapDirArgs decides the deferred callback's parameter name and the value passed
// through to it. If OnDriverInit declares a usable first parameter, reuse it;
// otherwise default the callback param to strDIR and pass nothing.
func wrapDirArgs(params string) (innerParam, passArg string) {
	for _, p := range strings.Split(params, ",") {
		p = strings.TrimSpace(p)
		if p != "" && p != "..." {
			return p, ", " + p
		}
		break
	}
	return "strDIR", ""
}

// reindent prefixes every non-blank line of body with pad, trimming surrounding
// blank lines so the wrapped body sits one level deeper without stray gaps.
// Lines that begin inside a multi-line string literal are left untouched —
// prepending pad there would change the string's value (C4 drivers commonly
// embed XML/HTML in [[ … ]] blocks).
func reindent(body, pad string) string {
	body = strings.Trim(body, "\n")
	if strings.TrimSpace(body) == "" {
		return ""
	}
	spans := longSpans([]byte(body))
	insideLong := func(off int) bool {
		for _, s := range spans {
			if off >= s[0] && off < s[1] {
				return true
			}
		}
		return false
	}
	lines := strings.Split(body, "\n")
	off := 0
	for i, ln := range lines {
		lineStart := off
		off += len(ln) + 1 // +1 for the '\n' that Split removed
		switch {
		case strings.TrimSpace(ln) == "":
			lines[i] = ""
		case insideLong(lineStart):
			// continuation of a [[ … ]] literal — its indentation is content
		default:
			lines[i] = pad + ln
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

// newOnDriverInit renders a fresh OnDriverInit when the driver has none.
func newOnDriverInit(d Decision) []byte {
	// Pass nothing to the callback (no strDIR): reading an undefined global here
	// is nil normally but a hard error under a strict-globals _G __index — a
	// common C4 pattern. Mirrors wrapDirArgs' no-parameter case.
	return []byte("\n\nfunction OnDriverInit(...)\n" +
		"    " + sdkRequire(d) + "\n" +
		"    " + d.initCall() + "\n\n" +
		"    Anvil:OnDriverInit(function(strDIR)\n" +
		"    end)\nend\n")
}

// ensureConfigRequire prepends require('config') for the build-target case (the
// config lives in the CONFIG table from the swapped-in config.lua) when it isn't
// already there.
func ensureConfigRequire(src []byte, d Decision) []byte {
	// Match against code only: a commented-out `-- require('config')` must not
	// suppress inserting the real one for a build-targets driver.
	if !d.BuildTargets || configRequireRe.Match(stripComments(src)) {
		return src
	}
	return append([]byte("require('config')\n"), src...)
}

var (
	driverTagRe     = regexp.MustCompile(`<Driver\b[^>]*>`)
	squishLuaAttrRe = regexp.MustCompile(`squishLua\s*=\s*"[^"]*"`)
	closeItemsRe    = regexp.MustCompile(`(?m)^([ \t]*)</Items>`)
)

// PatchManifest ensures the manifest squishes Lua and ships the vendor dir. It
// returns the new bytes (equal to input when already satisfied).
// Squishy Module lines for the files init's patches require. The module NAMES
// must match the require strings PatchEntry/ensureConfigRequire write verbatim
// — the squisher registers each as a package.preload key, and a require that
// isn't declared here is a driver-load LUA_ERROR on the controller.
const squishyConfigModule = `Module "config" "config.lua"`

const vendorItem = `<Item type="dir" c4zDir="vendor" name="vendor" recurse="true" exclude="false"/>`

func squishySDKModule(dirs ...string) string {
	return `Module "` + sdkModule(dirs...) + `" "` + sdkRelPath(dirs...) + `"`
}

// PatchSquishy ensures the squishy declares the modules init introduces: the
// embedded SDK, and — with build targets — the config module. Missing Module
// lines are inserted before the Output directive (or appended); existing
// declarations (any module name pointing at the file) are left alone.
func PatchSquishy(src []byte, buildTargets bool, dirs ...string) []byte {
	var add []string
	if !bytes.Contains(src, []byte(`"`+sdkRelPath(dirs...)+`"`)) {
		add = append(add, squishySDKModule(dirs...))
	}
	if buildTargets && !bytes.Contains(src, []byte(`"config.lua"`)) {
		add = append(add, squishyConfigModule)
	}
	if len(add) == 0 {
		return src
	}

	block := strings.Join(add, "\n") + "\n"
	lines := strings.SplitAfter(string(src), "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "Output") {
			return []byte(strings.Join(lines[:i], "") + block + "\n" + strings.Join(lines[i:], ""))
		}
	}
	out := string(src)
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return []byte(out + "\n" + block)
}

// NewSquishy renders a fresh squishy for a driver that has none — init forces
// squishLua on the manifest, so the build needs one to exist.
func NewSquishy(entryRel string, buildTargets bool, dirs ...string) []byte {
	var b strings.Builder
	b.WriteString(`Main "` + filepath.ToSlash(entryRel) + `"` + "\n\n")
	b.WriteString(squishySDKModule(dirs...) + "\n")
	if buildTargets {
		b.WriteString(squishyConfigModule + "\n")
	}
	return []byte(b.String())
}

// PatchManifest adds what Driverforge needs to an existing manifest.c4zproj:
// squishLua, so the embedded SDK is bundled into the driver, and a vendor item so
// the SDK's files ship with it. Both edits are idempotent — a manifest that
// already declares them is returned unchanged — because init may run against a
// project someone set up by hand.
//
// It returns the patched source rather than a parsed document: the file is edited
// textually so the author's formatting and comments survive.
func PatchManifest(src []byte, dirs ...string) ([]byte, error) {
	d, err := manifest.ParseBytes(src, "manifest.c4zproj")
	if err != nil {
		return nil, fmt.Errorf("parsing manifest: %w", err)
	}

	out := src
	if !d.SquishLua {
		out = ensureSquishLua(out)
	}
	if !hasSDKItem(d, sdkDir(dirs...)) {
		out = insertSDKItem(out, sdkDir(dirs...))
	}
	return out, nil
}

func hasDirItem(d *manifest.Driver, dir string) bool {
	for _, it := range d.Items {
		if filepath.ToSlash(it.Name) == filepath.ToSlash(dir) {
			return true
		}
	}
	return false
}

func hasSDKItem(d *manifest.Driver, dir string) bool {
	if dir == "." {
		for _, item := range d.Items {
			if item.Type == manifest.ItemFile && item.Name == sdkrelease.BundleName {
				return true
			}
		}
		return false
	}
	return hasDirItem(d, dir)
}

func hasVendorItem(d *manifest.Driver) bool { return hasDirItem(d, "vendor") }

// ensureSquishLua sets squishLua="true" on the <Driver> tag, replacing an
// existing (false) value or adding the attribute when absent.
func ensureSquishLua(src []byte) []byte {
	tag := driverTagRe.Find(src)
	if tag == nil {
		return src
	}
	var newTag []byte
	if squishLuaAttrRe.Match(tag) {
		newTag = squishLuaAttrRe.ReplaceAll(tag, []byte(`squishLua="true"`))
	} else {
		// Insert before the closing '>' (or '/>').
		insertAt := len(tag) - 1
		if len(tag) >= 2 && tag[len(tag)-2] == '/' {
			insertAt = len(tag) - 2
		}
		newTag = append(append(append([]byte{}, tag[:insertAt]...), []byte(` squishLua="true"`)...), tag[insertAt:]...)
	}
	// Literal single replacement: Regexp.ReplaceAll would expand $-sequences in
	// newTag (a Driver tag with name="promo$100" would lose "$100") and would
	// rewrite every <Driver…> occurrence, not just the one we matched.
	return bytes.Replace(src, tag, newTag, 1)
}

// insertSDKItem adds the SDK <Item> just before </Items>, matching its
// indentation (plus one nesting level).
func insertSDKItem(src []byte, dir string) []byte {
	loc := closeItemsRe.FindSubmatchIndex(src)
	if loc == nil {
		return src
	}
	indent := string(src[loc[2]:loc[3]])
	itemIndent := indent + "  "
	var item string
	if dir == "." {
		item = `<Item type="file" name="` + sdkrelease.BundleName + `"/>`
	} else {
		dir = filepath.ToSlash(dir)
		item = `<Item type="dir" c4zDir="` + dir + `" name="` + dir + `" recurse="true" exclude="false"/>`
	}
	insertion := itemIndent + item + "\n"
	out := make([]byte, 0, len(src)+len(insertion))
	out = append(out, src[:loc[0]]...)
	out = append(out, insertion...)
	out = append(out, src[loc[0]:]...)
	return out
}
