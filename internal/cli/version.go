package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/driverforge/cli/internal/agent"
	"github.com/driverforge/cli/internal/buildinfo"
	"github.com/driverforge/cli/internal/driverforgeapi"
	"github.com/driverforge/cli/internal/driverforgeconfig"
	"github.com/driverforge/cli/internal/orgctx"
	"github.com/driverforge/cli/internal/project"
	"github.com/driverforge/cli/internal/ui"
)

// versionHealthTimeout bounds the best-effort /healthz probe so `version` (and
// the upgrade table's agent row) stays snappy when the selected controller is
// offline.
const versionHealthTimeout = 4 * time.Second

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the driverforge version, the driver's SDK, and the selected controller's agent and Director",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if structured() {
				return emit(cmd, versionReport(cmd.Context()), noText)
			}
			return runVersion(cmd.Context(), cmd.OutOrStdout())
		},
	}
}

// The version report groups what it shows under the thing that owns each
// piece — no collective label: the CLI and the graph service's deploy stamp
// under "Driverforge", the embedded SDK under the driver's name, and the
// agent + Director under the controller's name. Interactive, the static rows
// render immediately and the two network rows (graph, controller) resolve
// live spinners in place; non-interactive, both probes run concurrently and
// the finished report prints once.
func runVersion(ctx context.Context, out io.Writer) error {
	groups, dev, hasDev := versionGroups()
	if !ui.Interactive(flagNoTUI) {
		printVersionPlain(ctx, out, groups, dev, hasDev)
		return nil
	}
	return ui.RunVersionView(ctx, groups, func(send func(ui.VersionUpdateMsg)) {
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			send(ui.VersionUpdateMsg{ID: "driverforge", Rows: driverforgeRows(ctx)})
		}()
		if hasDev {
			wg.Add(1)
			go func() {
				defer wg.Done()
				name, rows := controllerRows(ctx, dev)
				send(ui.VersionUpdateMsg{ID: "controller", Name: name, Rows: rows})
			}()
		}
		wg.Wait()
	})
}

// versionGroups assembles the report's initial state: static rows filled in,
// network-dependent ones pending. dev/hasDev report the selected controller,
// whose group resolves separately.
func versionGroups() (groups []ui.VersionGroup, dev orgctx.Device, hasDev bool) {
	groups = []ui.VersionGroup{{
		ID:   "driverforge",
		Name: "Driverforge",
		Rows: []ui.VersionRow{
			{Label: "CLI", Value: cliVersionValue()},
			{Label: "Cloud", Pending: true},
		},
	}}

	if proj, err := project.Locate(flagProjectDir); err == nil {
		if cfg, err := driverforgeconfig.Load(proj.Dir); err == nil && cfg != nil && cfg.SDKVersion != "" {
			driver := firstNonEmpty(cfg.Project.Name, firstNonEmpty(cfg.Project.Slug, filepath.Base(proj.Dir)))
			groups = append(groups, ui.VersionGroup{
				ID:   "driver",
				Name: driver,
				Rows: []ui.VersionRow{{Label: "Driverforge SDK", Value: cfg.SDKVersion}},
			})
		}
	}

	if d, err := orgctx.CurrentDevice(); err == nil {
		dev, hasDev = d, true
		groups = append(groups, ui.VersionGroup{
			ID:   "controller",
			Name: dev.Name,
			Rows: []ui.VersionRow{{Label: "Anvil Agent", Pending: true}},
		})
	}
	return groups, dev, hasDev
}

func cliVersionValue() string {
	return strings.TrimPrefix(buildinfo.String(), "driverforge ")
}

// driverforgeRows resolves the Driverforge group: the CLI row plus, when the
// graph answers, its deploy stamp. An unreachable graph just drops the row.
func driverforgeRows(ctx context.Context) []ui.VersionRow {
	rows := []ui.VersionRow{{Label: "CLI", Value: cliVersionValue()}}
	if rel, ok := cloudRelease(ctx); ok {
		rows = append(rows, ui.VersionRow{Label: "Cloud", Value: rel})
	}
	return rows
}

// cloudRelease is the graph service's deploy stamp; ok is false when it
// doesn't answer.
func cloudRelease(ctx context.Context) (string, bool) {
	gctx, cancel := context.WithTimeout(ctx, versionHealthTimeout)
	defer cancel()
	rel, err := driverforgeapi.New().Release(gctx)
	if err != nil {
		return "", false
	}
	// An unstamped graph (local dev) reports itself with an empty release —
	// name it rather than dropping it.
	return firstNonEmpty(rel, "dev (unstamped)"), true
}

// controllerRows resolves the controller group from its /healthz: the agent +
// Director versions, and the controller's self-reported name. A controller
// that can't be addressed or doesn't answer reports as unreachable.
func controllerRows(ctx context.Context, dev orgctx.Device) (name string, rows []ui.VersionRow) {
	unreachable := []ui.VersionRow{{Label: "Anvil Agent", Value: "unreachable"}}
	h, err := probeController(ctx, dev)
	if err != nil {
		if errors.Is(err, agent.ErrFingerprintMismatch) {
			return dev.Name, []ui.VersionRow{{Label: "Anvil Agent", Value: "TLS certificate mismatch"}}
		}
		return dev.Name, unreachable
	}
	rows = []ui.VersionRow{{Label: "Anvil Agent", Value: firstNonEmpty(h.AgentVersion, "unknown")}}
	if h.ControllerVersion != "" {
		rows = append(rows, ui.VersionRow{Label: "Director", Value: h.ControllerVersion})
	}
	return controllerLabel(dev, h), rows
}

// printVersionPlain resolves both network probes concurrently and prints the
// finished report once — pipes and CI get no spinners.
func printVersionPlain(ctx context.Context, out io.Writer, groups []ui.VersionGroup, dev orgctx.Device, hasDev bool) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		update(groups, "driverforge", "", driverforgeRows(ctx))
	}()
	if hasDev {
		wg.Add(1)
		go func() {
			defer wg.Done()
			name, rows := controllerRows(ctx, dev)
			update(groups, "controller", name, rows)
		}()
	}
	wg.Wait()

	width := 0
	for _, g := range groups {
		for _, r := range g.Rows {
			if len(r.Label) > width {
				width = len(r.Label)
			}
		}
	}
	for i, g := range groups {
		if i > 0 {
			fmt.Fprintln(out)
		}
		fmt.Fprintln(out, g.Name)
		for _, r := range g.Rows {
			fmt.Fprintf(out, "  %-*s  %s\n", width, r.Label, r.Value)
		}
	}
}

// update replaces a group's rows (and optionally its name) in place.
func update(groups []ui.VersionGroup, id, name string, rows []ui.VersionRow) {
	for i := range groups {
		if groups[i].ID == id {
			groups[i].Rows = rows
			if name != "" {
				groups[i].Name = name
			}
		}
	}
}

// probeController asks the controller's agent for its /healthz, bounded so an
// offline controller can't stall the report.
func probeController(ctx context.Context, dev orgctx.Device) (agent.Health, error) {
	hctx, cancel := context.WithTimeout(ctx, versionHealthTimeout)
	defer cancel()
	_, h, err := dialAgent(hctx, dev)
	return h, err
}

// versionOut is what version reports in structured output.
type versionOut struct {
	CLI cliVersionOut `json:"cli"`
	// Cloud is the Driverforge service's release, or nil when it didn't answer.
	Cloud      *string               `json:"cloud"`
	Driver     *driverVersionOut     `json:"driver"`
	Controller *controllerVersionOut `json:"controller"`
}

type cliVersionOut struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
}

// driverVersionOut is the driver in the working directory, when it's set up
// for Driverforge.
type driverVersionOut struct {
	Name       string `json:"name"`
	SDKVersion string `json:"sdk_version"`
}

// controllerVersionOut is the selected controller. Status is "ok",
// "unreachable" or "certificate_mismatch"; the versions are nil unless ok.
type controllerVersionOut struct {
	Device          deviceRefOut `json:"device"`
	Status          string       `json:"status"`
	AgentVersion    *string      `json:"agent_version"`
	DirectorVersion *string      `json:"director_version"`
}

// versionReport probes the cloud and the selected controller concurrently and
// reports everything version shows.
func versionReport(ctx context.Context) versionOut {
	o := versionOut{CLI: cliVersionOut{Version: buildinfo.Version, Commit: buildinfo.Commit, Date: buildinfo.Date}}
	if proj, err := project.Locate(flagProjectDir); err == nil {
		if cfg, err := driverforgeconfig.Load(proj.Dir); err == nil && cfg != nil && cfg.SDKVersion != "" {
			name := firstNonEmpty(cfg.Project.Name, firstNonEmpty(cfg.Project.Slug, filepath.Base(proj.Dir)))
			o.Driver = &driverVersionOut{Name: name, SDKVersion: cfg.SDKVersion}
		}
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if rel, ok := cloudRelease(ctx); ok {
			o.Cloud = &rel
		}
	}()
	if dev, err := orgctx.CurrentDevice(); err == nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h, err := probeController(ctx, dev)
			c := &controllerVersionOut{Device: deviceRefOf(dev, h), Status: "ok"}
			switch {
			case errors.Is(err, agent.ErrFingerprintMismatch):
				c.Status = "certificate_mismatch"
			case err != nil:
				c.Status = "unreachable"
			default:
				c.AgentVersion = optional(firstNonEmpty(h.AgentVersion, "unknown"))
				c.DirectorVersion = optional(h.ControllerVersion)
			}
			o.Controller = c
		}()
	}
	wg.Wait()
	return o
}
