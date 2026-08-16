package ui

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/driverforge/cli/internal/clierr"
)

func TestRenderUserErrorPlainNonTTY(t *testing.T) {
	var buf bytes.Buffer // not an *os.File → non-terminal → plain output
	RenderUserError(&buf, clierr.User("not a driver project", "run `driverforge init`"))
	out := buf.String()
	if !strings.HasPrefix(out, "error: ") {
		t.Errorf("expected plain 'error:' prefix, got %q", out)
	}
	if !strings.Contains(out, "not a driver project") {
		t.Errorf("message missing: %q", out)
	}
	if !strings.Contains(out, "tip: run `driverforge init`") {
		t.Errorf("tip line missing: %q", out)
	}
	if strings.Contains(out, "\x1b[") {
		t.Errorf("non-TTY output must not contain ANSI: %q", out)
	}
}

func TestRenderCrashPlainNonTTY(t *testing.T) {
	var buf bytes.Buffer
	RenderCrash(&buf, errors.New("graph api: HTTP 404"))
	out := buf.String()
	if !strings.HasPrefix(out, "error: ") {
		t.Errorf("expected plain 'error:' prefix, got %q", out)
	}
	if !strings.Contains(out, "graph api: HTTP 404") {
		t.Errorf("message missing: %q", out)
	}
	if strings.Contains(out, "\x1b[") {
		t.Errorf("non-TTY output must not contain ANSI: %q", out)
	}
}

func TestRenderNil(t *testing.T) {
	var buf bytes.Buffer
	RenderUserError(&buf, nil)
	RenderCrash(&buf, nil)
	if buf.Len() != 0 {
		t.Errorf("nil error should render nothing, got %q", buf.String())
	}
}

func TestDissect(t *testing.T) {
	// A UserError supplies its own title, message and hint.
	ue := clierr.Wrap(nil, "not a project", "run `driverforge init`")
	ue.Title = "No project found"
	title, msg, hint := dissect(ue)
	if title != "No project found" || msg != "not a project" || hint != "run `driverforge init`" {
		t.Errorf("UserError dissect = %q / %q / %q", title, msg, hint)
	}
	// A UserError with no title falls back to a friendly default.
	if title, _, _ := dissect(clierr.User("x", "")); title != "Heads up" {
		t.Errorf("default title = %q, want \"Heads up\"", title)
	}
	// A legacy hint-carrying error is split on its "(run `driverforge …`)" suffix.
	_, msg, hint = dissect(errors.New("not logged in (run `driverforge login`)"))
	if msg != "not logged in" || hint != "run `driverforge login`" {
		t.Errorf("legacy dissect = %q / %q", msg, hint)
	}
}

// The framed card (login panel) must line up: every rendered line shares one
// display width, whatever the title length or an emoji in a row.
func TestCardAligned(t *testing.T) {
	out := card(colInfo, "Sign in", []string{
		textRow(colInk, true, `Unknown command "foo" for "driverforge"`),
		"",
		textRow(colTip, true, "💡  "+linkify("see https://go.driverforge.com/cli")),
	})

	lines := strings.Split(out, "\n")
	if len(lines) < 3 {
		t.Fatalf("card too short: %q", out)
	}
	want := lipgloss.Width(lines[0])
	for i, ln := range lines {
		if w := lipgloss.Width(ln); w != want {
			t.Errorf("line %d width = %d, want %d: %q", i, w, want, ln)
		}
	}
	if !strings.Contains(out, "SIGN IN") {
		t.Errorf("title tab missing: %q", out)
	}
}

// The callout block is a left-bordered message: every line carries the accent
// bar, and there is no frame or title.
func TestCalloutBlock(t *testing.T) {
	out := calloutBlock(colAmber, []string{
		blockRow(colInk, true, `Unknown command "devices" for "driverforge"`),
		"",
		blockRow(colTip, true, " 💡  see https://go.driverforge.com/cli"),
	})

	for i, ln := range strings.Split(out, "\n") {
		if !strings.Contains(ln, "┃") {
			t.Errorf("line %d missing the accent bar: %q", i, ln)
		}
	}
	if strings.ContainsAny(out, "╭╰╮╯") {
		t.Errorf("callout block must not be framed: %q", out)
	}
	if !strings.Contains(out, `Unknown command "devices"`) {
		t.Errorf("message missing: %q", out)
	}
}

func TestLinkify(t *testing.T) {
	const url = "https://go.driverforge.com/build"
	out := linkify("see " + url)
	// The URL becomes an OSC-8 hyperlink (link + visible name both the URL)...
	if !strings.Contains(out, "\x1b]8;;"+url) {
		t.Errorf("expected OSC-8 hyperlink, got %q", out)
	}
	// ...but the visible width is unchanged (escapes are zero-width).
	if got, want := lipgloss.Width(out), lipgloss.Width("see "+url); got != want {
		t.Errorf("linkify changed width: got %d want %d", got, want)
	}
	// Text with no URL is returned untouched.
	if got := linkify("run `driverforge init`"); got != "run `driverforge init`" {
		t.Errorf("linkify mangled non-URL text: %q", got)
	}
}

func TestSplitHint(t *testing.T) {
	h, hint := splitHint("not logged in (run `driverforge login`)")
	if h != "not logged in" || hint != "run `driverforge login`" {
		t.Errorf("splitHint = %q / %q", h, hint)
	}
	h, hint = splitHint("graph api: HTTP 404")
	if h != "graph api: HTTP 404" || hint != "" {
		t.Errorf("no-hint case = %q / %q", h, hint)
	}
}
