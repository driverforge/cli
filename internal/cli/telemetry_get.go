package cli

import (
	"context"
	"io"
	"regexp"
	"slices"
	"time"

	"github.com/spf13/cobra"

	"github.com/driverforge/cli/internal/driverforgeapi"
	"github.com/driverforge/cli/internal/telemetryread"
)

// newGetCmd fetches one entry, of any type, in full.
func newGetCmd() *cobra.Command {
	var (
		scope      scopeFlags
		kind       string
		output     string
		maxPayload int
	)
	cmd := &cobra.Command{
		Use:   "get <id>",
		Short: "Fetch one log, error or event with its full payload, or an error group",
		Long: `Fetch one telemetry entry by id with its full, untruncated payload — the
command a payload_truncated warning lists for each entry it cut. An event's id
is its trace id.

An error group is every occurrence of one error. Fetch it by its reference,
<PROJECT-SLUG>-<FINGERPRINT> (for example POOL-PUMP-4F2A9C1B), the form the
web app shows and people quote. The reference names the project, and a group is
found however long ago it last occurred.`,
		Example: "  driverforge get 6f1c2f9e-6c1a-4c39-9a51-0d4b8e1f2a3b\n  driverforge get 0123456789abcdef0123456789abcdef --type event -o json\n  driverforge get POOL-PUMP-4F2A9C1B --type error_group",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGet(cmd, args[0], scope, kind, output, maxPayload)
		},
	}
	addScopeFlags(cmd, &scope)
	cmd.Flags().StringVar(&kind, "type", "", "entry type, to skip looking it up: log, error, event or error_group")
	cmd.Flags().StringVarP(&output, "output", "o", "", "output format: text or json")
	cmd.Flags().IntVar(&maxPayload, "max-payload-bytes", 0, "cap the message or payload; 0 (the default) for no cap")
	return cmd
}

var (
	uuidRE    = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	traceIDRE = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)
	allKinds  = []telemetryread.Kind{telemetryread.KindLog, telemetryread.KindError, telemetryread.KindEvent}
)

func runGet(cmd *cobra.Command, id string, sf scopeFlags, kind, output string, maxPayload int) error {
	stdout := cmd.OutOrStdout()
	diag := diagnostics{w: cmd.ErrOrStderr(), json: structured()}
	format, err := getFormat(output, stdout)
	if err == nil {
		err = validateGet(id, kind, maxPayload)
	}
	if err != nil {
		return reportFailure(err, format, stdout, diag)
	}
	if ref, ok := parseErrorRef(id); ok && (kind == kindErrorGroup || kind == "") {
		return runGetGroup(cmd.Context(), ref, sf, format, stdout, diag)
	}

	ctx := cmd.Context()
	api := driverforgeapi.New()
	scope, err := resolveScope(ctx, api, sf, false)
	if err != nil {
		return reportFailure(err, format, stdout, diag)
	}
	entry, err := findEntry(ctx, api, scope, id, getKinds(id, kind))
	if err != nil {
		return reportFailure(err, format, stdout, diag)
	}
	writeGet(stdout, diag, format, scope, entry, maxPayload)
	return nil
}

// getFormat is get's output format: one entry is one document, so jsonl is
// json.
func getFormat(output string, stdout io.Writer) (string, error) {
	format, err := outputFormat(output, false, stdout)
	if err != nil || format == formatJSONL {
		return formatJSON, err
	}
	return format, nil
}

func validateGet(id, kind string, maxPayload int) error {
	switch {
	case maxPayload < 0:
		return newCmdErr(codeUsage, "--max-payload-bytes can't be negative.", "Pass a byte count, or 0 for no cap.")
	case kind != "" && kind != kindErrorGroup && !slices.Contains(allKinds, telemetryread.Kind(kind)):
		return newCmdErr(codeUsage, "--type must be log, error, event or error_group.", "Pass --type log, error, event or error_group.")
	case kind == kindErrorGroup:
		return validateGroupGet(id, maxPayload)
	case kind == "" && refRE.MatchString(id):
		return validateGroupGet(id, maxPayload)
	case !uuidRE.MatchString(id) && !traceIDRE.MatchString(id):
		return newCmdErr(codeUsage, "That isn't an entry id.", "Pass a log or error id (a UUID), an event's 32-character trace id, or an error group's reference such as POOL-PUMP-4F2A9C1B.").with("id", id)
	}
	return nil
}

// getKinds is the types to look an id up as, in order: --type when given;
// otherwise logs and errors have UUIDs and events 32-hex trace ids.
func getKinds(id, kind string) []telemetryread.Kind {
	switch {
	case kind != "":
		return []telemetryread.Kind{telemetryread.Kind(kind)}
	case uuidRE.MatchString(id):
		return allKinds
	}
	return []telemetryread.Kind{telemetryread.KindEvent}
}

func findEntry(ctx context.Context, api *driverforgeapi.Client, scope *telemetryScope, id string, kinds []telemetryread.Kind) (telemetryread.Entry, error) {
	for _, k := range kinds {
		e, found, err := fetchEntry(ctx, api, scope, k, id)
		if err != nil || found {
			return e, err
		}
	}
	return telemetryread.Entry{}, newCmdErr(codeEntryNotFound, "No entry with that id in "+scope.project.Slug+".",
		"Check the id and --project; entries older than your plan's retention are gone.").with("id", id)
}

// fetchEntry looks id up as one type. Logs and errors are keyed by UUID, so
// any other id is not one of theirs.
func fetchEntry(ctx context.Context, api *driverforgeapi.Client, scope *telemetryScope, k telemetryread.Kind, id string) (telemetryread.Entry, bool, error) {
	projects := []string{scope.project.ID}
	if k != telemetryread.KindEvent && !uuidRE.MatchString(id) {
		return telemetryread.Entry{}, false, nil
	}
	switch k {
	case telemetryread.KindLog:
		l, err := api.Log(ctx, projects, id)
		if err != nil || l == nil {
			return telemetryread.Entry{}, false, err
		}
		return telemetryread.FromLog(*l, scope.names), true, nil
	case telemetryread.KindError:
		x, err := api.ErrorOccurrence(ctx, projects, id)
		if err != nil || x == nil {
			return telemetryread.Entry{}, false, err
		}
		return telemetryread.FromError(*x, scope.names), true, nil
	}
	x, err := api.Event(ctx, projects, id)
	if err != nil || x == nil {
		return telemetryread.Entry{}, false, err
	}
	return telemetryread.FromEvent(*x, scope.names), true, nil
}

func writeGet(stdout io.Writer, diag diagnostics, format string, scope *telemetryScope, entry telemetryread.Entry, maxPayload int) {
	entries := []telemetryread.Entry{entry}
	var warnings []warning
	if w := truncateEntries(entries, maxPayload, scope); w != nil {
		warnings = append(warnings, *w)
	}
	so := scope.out(time.Time{}, time.Time{})
	if format == formatJSON {
		writeJSON(stdout, map[string]any{"scope": so, "entry": entries[0], "warnings": nonNilWarnings(warnings)})
		return
	}
	diag.scope(so)
	writeTextEntry(stdout, entries[0])
	for _, w := range warnings {
		diag.warn(w)
	}
}
