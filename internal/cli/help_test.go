package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// runHelp runs args and returns stdout and stderr.
func runHelp(t *testing.T, args ...string) (string, string) {
	t.Helper()
	root := newRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"--no-update-check"}, args...))
	if _, err := root.ExecuteC(); err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return stdout.String(), stderr.String()
}

// TestStructuredHelpForEveryCommand holds every command to the structured
// contract: its help is one JSON document on stdout, nothing on stderr.
func TestStructuredHelpForEveryCommand(t *testing.T) {
	t.Setenv("DRIVERFORGE_OUTPUT", "json")
	var walk func(c *cobra.Command)
	n := 0
	walk = func(c *cobra.Command) {
		if !c.IsAvailableCommand() && c.HasParent() {
			return
		}
		path := strings.Fields(c.CommandPath())[1:]
		stdout, stderr := runHelp(t, append(path, "--help")...)
		var doc helpOut
		if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
			t.Errorf("%s --help: not one JSON document: %v\n%s", c.CommandPath(), err, stdout)
		} else if doc.Command != c.CommandPath() {
			t.Errorf("%s --help: command = %q", c.CommandPath(), doc.Command)
		}
		if stderr != "" {
			t.Errorf("%s --help wrote to stderr: %s", c.CommandPath(), stderr)
		}
		n++
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(newRootCmd())
	if n < 20 {
		t.Fatalf("walked only %d commands", n)
	}
}

func TestStructuredHelpShape(t *testing.T) {
	t.Setenv("DRIVERFORGE_OUTPUT", "json")
	stdout, _ := runHelp(t, "help", "logs")
	var doc helpOut
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatal(err)
	}
	var timeout, output *helpFlag
	for i, f := range doc.Flags {
		switch f.Name {
		case "timeout":
			timeout = &doc.Flags[i]
		case "output":
			output = &doc.Flags[i]
		case "help":
			t.Error("the help flag is listed")
		}
	}
	if timeout == nil || timeout.Type != "duration" || timeout.Shorthand != nil {
		t.Errorf("timeout = %+v", timeout)
	}
	if output == nil || output.Shorthand == nil || *output.Shorthand != "o" {
		t.Errorf("output = %+v", output)
	}
	if len(doc.Examples) == 0 || !strings.HasPrefix(doc.Examples[0], "driverforge logs") {
		t.Errorf("examples = %q", doc.Examples)
	}
	found := false
	for _, f := range doc.GlobalFlags {
		found = found || f.Name == "project-dir"
	}
	if !found {
		t.Error("global flags don't include --project-dir")
	}

	stdout, _ = runHelp(t, "skills")
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil || len(doc.Subcommands) != 3 {
		t.Errorf("bare group: %v, %+v", err, doc.Subcommands)
	}
}

func TestTextHelpUnchanged(t *testing.T) {
	stdout, _ := runHelp(t, "logs", "--help")
	if !strings.Contains(stdout, "Usage:\n  driverforge logs [flags]") || strings.HasPrefix(stdout, "{") {
		t.Errorf("text help changed:\n%s", stdout)
	}
}
