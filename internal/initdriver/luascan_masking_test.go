package initdriver

import (
	"strings"
	"testing"
)

// stripComments blanks comments but must preserve string literals — the checks
// it feeds match a string argument (require('config'), require('vendor.driverforge-sdk')).
func TestStripComments(t *testing.T) {
	src := []byte(`x = "a -- not a comment"  -- real comment
require('config') -- trailing
--[[ long
comment ]] y = 1`)
	got := string(stripComments(src))
	if strings.Contains(got, "real comment") || strings.Contains(got, "trailing") {
		t.Errorf("line comments not stripped: %q", got)
	}
	if strings.Contains(got, "long") || strings.Contains(got, "comment ]]") {
		t.Errorf("long comment not stripped: %q", got)
	}
	// String contents and code survive.
	if !strings.Contains(got, `"a -- not a comment"`) {
		t.Errorf("string literal must survive comment stripping: %q", got)
	}
	if !strings.Contains(got, "require('config')") {
		t.Errorf("real require must survive: %q", got)
	}
	if !strings.Contains(got, "y = 1") {
		t.Errorf("code after a long comment must survive: %q", got)
	}
	if len(got) != len(src) {
		t.Errorf("byte offsets must be preserved: got %d want %d", len(got), len(src))
	}
}

// CodeOnly blanks comments AND strings, leaving only code identifiers.
func TestCodeOnly(t *testing.T) {
	src := []byte(`Log:Info("Logger.debug in a string") -- Logger.trace in a comment`)
	got := string(CodeOnly(src))
	if !strings.Contains(got, "Log:Info") {
		t.Errorf("code call must survive: %q", got)
	}
	// The Logger references live only in a string and a comment — both gone.
	if strings.Contains(got, "Logger") {
		t.Errorf("string/comment content must be blanked: %q", got)
	}
	if len(got) != len(src) {
		t.Errorf("byte offsets must be preserved: got %d want %d", len(got), len(src))
	}
}

// A -- or ]] inside a string must not be misread as a comment/long-string, even
// when the string itself isn't blanked (stripComments path).
func TestStripCommentsDoesNotMisreadDelimitersInStrings(t *testing.T) {
	src := []byte(`a = "text ]] with -- inside"` + "\n" + `b = 2`)
	got := string(stripComments(src))
	if !strings.Contains(got, "b = 2") {
		t.Errorf("code after a tricky string must survive intact: %q", got)
	}
}
