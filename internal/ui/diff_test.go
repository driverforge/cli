package ui

import (
	"strings"
	"testing"
)

func TestLineDiffBasic(t *testing.T) {
	a := []string{"one", "two", "three"}
	b := []string{"one", "TWO", "three"}
	ops := lineDiff(a, b)

	var got string
	for _, o := range ops {
		got += string(o.kind) + o.text + "|"
	}
	// two replaced: a deletion of "two" and an addition of "TWO", context kept.
	if !strings.Contains(got, "-two|") || !strings.Contains(got, "+TWO|") {
		t.Errorf("unexpected ops: %s", got)
	}
	if !strings.Contains(got, " one|") || !strings.Contains(got, " three|") {
		t.Errorf("context lines missing: %s", got)
	}
}

func TestRenderFileDiffNewFileShowsAdditions(t *testing.T) {
	out := RenderFileDiff("src/.gitignore", nil, []byte(".env.*.lua\n!.env.sample.lua\n"))
	if !strings.Contains(out, "new file") {
		t.Errorf("expected new-file header: %s", out)
	}
	if !strings.Contains(out, ".env.*.lua") || !strings.Contains(out, "!.env.sample.lua") {
		t.Errorf("expected additions shown: %s", out)
	}
}

func TestRenderFileDiffLargeNewFileSummarised(t *testing.T) {
	var lines []string
	for i := 0; i < createPreviewMax+10; i++ {
		lines = append(lines, "line")
	}
	out := RenderFileDiff("src/vendor/driverforge-sdk.lua", nil, []byte(strings.Join(lines, "\n")+"\n"))
	if !strings.Contains(out, "contents omitted") {
		t.Errorf("expected large new file to be summarised: %s", out)
	}
}

func TestRenderFileDiffModifyCollapsesContext(t *testing.T) {
	var oldLines, newLines []string
	for i := 0; i < 30; i++ {
		oldLines = append(oldLines, "ctx")
		newLines = append(newLines, "ctx")
	}
	newLines[15] = "changed"
	out := RenderFileDiff("src/driver.lua", []byte(strings.Join(oldLines, "\n")), []byte(strings.Join(newLines, "\n")))
	if !strings.Contains(out, "⋯") {
		t.Errorf("expected collapsed-context separator: %s", out)
	}
	if !strings.Contains(out, "changed") {
		t.Errorf("expected the change shown: %s", out)
	}
	// Far-away context (line 0) should be collapsed away, not printed 30 times.
	if strings.Count(out, "ctx") > 2*diffContext+2 {
		t.Errorf("context not collapsed:\n%s", out)
	}
}
