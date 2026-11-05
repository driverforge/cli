// Package agentmode detects when the CLI is being driven by a coding agent
// rather than a person, so commands can switch to output an agent parses
// reliably: no colour, no spinners, no prompts, no update notices, and
// structured diagnostics.
package agentmode

import (
	"os"
	"strconv"
	"strings"
)

// agentEnv are variables coding agents set in the shells they run. Presence
// with a non-empty value is enough; their values are not a contract.
var agentEnv = []string{
	"CLAUDECODE",    // Claude Code
	"CURSOR_AGENT",  // Cursor's agent terminal
	"GEMINI_CLI",    // Gemini CLI
	"CODEX_SANDBOX", // OpenAI Codex CLI
}

// Enabled reports whether the CLI is running under a coding agent.
// DRIVERFORGE_AGENT_MODE is authoritative when set to a boolean ("true",
// "false", "1", "0", …), so a person can force either mode; otherwise any of
// the known agent variables turns it on.
func Enabled() bool {
	if v, ok := os.LookupEnv("DRIVERFORGE_AGENT_MODE"); ok {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	for _, name := range agentEnv {
		if os.Getenv(name) != "" {
			return true
		}
	}
	return false
}

// Structured reports whether commands write structured output: one JSON
// document on stdout, JSON lines for progress and diagnostics on stderr, and
// no prompts. DRIVERFORGE_OUTPUT is authoritative when set to "json" or
// "text", so a script can ask for structured output without being an agent and
// a person can keep text output inside one; otherwise it follows Enabled.
func Structured() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("DRIVERFORGE_OUTPUT"))) {
	case "json":
		return true
	case "text":
		return false
	}
	return Enabled()
}
