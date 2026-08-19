package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/driverforgeapi"
	"github.com/driverforge/cli/internal/orgctx"
	"github.com/driverforge/cli/internal/ui"
)

func newOrgCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "org",
		Short: "Manage the active organization",
	}
	cmd.AddCommand(newOrgListCmd(), newOrgSelectCmd(), newOrgShowCmd(), newOrgCurrentCmd())
	return cmd
}

// newOrgListCmd lists the organizations the user belongs to, marking the current.
func newOrgListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List organizations you can access",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			orgs, err := driverforgeapi.New().ListOrganizations(cmd.Context())
			if err != nil {
				return err
			}
			if len(orgs) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "You don't belong to any organization yet.")
				return nil
			}
			current, _ := orgctx.Current()
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "\tNAME\tSLUG\tROLE")
			for _, o := range orgs {
				mark := " "
				if o.ID == current.ID {
					mark = "*"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", mark, o.Name, o.Slug, o.Role)
			}
			return tw.Flush()
		},
	}
}

// newOrgSelectCmd switches the active organization. With an argument it matches
// by slug, UUID, or exact name; with no argument it shows an interactive picker.
func newOrgSelectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "select [name|slug|id]",
		Short: "Select the active organization",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			arg := ""
			if len(args) == 1 {
				arg = args[0]
			}
			orgs, err := driverforgeapi.New().ListOrganizations(cmd.Context())
			if err != nil {
				return err
			}
			if len(orgs) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "You don't belong to any organization yet.")
				return nil
			}

			if arg != "" {
				org := findOrg(orgs, arg)
				if org == nil {
					return clierr.User(fmt.Sprintf("organization %q not found", arg), "run `driverforge org list`")
				}
				return applyOrg(cmd.OutOrStdout(), *org)
			}
			if !ui.Interactive(flagNoTUI) {
				return fmt.Errorf("specify an organization: `driverforge org select <slug>`")
			}
			remembered, _ := orgctx.Current()
			org, err := pickOrg(orgs, remembered.ID)
			if err != nil {
				return err
			}
			return applyOrg(cmd.OutOrStdout(), org)
		},
	}
}

// newOrgCurrentCmd prints just the active organization's slug — see
// newCurrentCmd for the shared porcelain contract.
func newOrgCurrentCmd() *cobra.Command {
	return newCurrentCmd("Print the active organization's slug (for scripts and prompts)",
		func() (string, error) {
			o, err := orgctx.Current()
			return o.Slug, err
		})
}

// newOrgShowCmd prints the active organization.
func newOrgShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show the active organization",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			o, err := orgctx.Current()
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), orgLabel(o.Name, o.Slug))
			return nil
		},
	}
}

// --- shared helpers (used by org commands and login) ---

// selectOrgAfterLogin resolves and persists the active org once logged in. It
// never returns an error that should fail login — callers treat it as a warning.
// pref (from `login --org`) forces a non-interactive choice when set.
func selectOrgAfterLogin(ctx context.Context, out io.Writer, pref string) error {
	orgs, err := driverforgeapi.New().ListOrganizations(ctx)
	if err != nil {
		return fmt.Errorf("could not list organizations: %w", err)
	}
	if len(orgs) == 0 {
		fmt.Fprintln(out, "You don't belong to any organization yet.")
		return nil
	}
	if pref != "" {
		org := findOrg(orgs, pref)
		if org == nil {
			return clierr.User(fmt.Sprintf("organization %q not found", pref), "run `driverforge org list` to see organizations")
		}
		return applyOrg(out, *org)
	}

	remembered := orgctx.Load().CurrentOrg
	rememberedID := ""
	if remembered != nil {
		rememberedID = remembered.ID
	}
	if ui.Interactive(flagNoTUI) && len(orgs) > 1 {
		org, err := pickOrg(orgs, rememberedID)
		if err != nil {
			return err
		}
		return applyOrg(out, org)
	}
	// Single org, or non-interactive: use remembered-or-first silently.
	def := orgctx.ChooseDefault(orgIDs(orgs), rememberedID)
	org := findByID(orgs, def)
	if org == nil {
		return nil
	}
	return applyOrg(out, *org)
}

// pickOrg shows the interactive picker, preselecting defID, and returns the org.
func pickOrg(orgs []driverforgeapi.Organization, defID string) (driverforgeapi.Organization, error) {
	choices := make([]ui.Choice, len(orgs))
	for i, o := range orgs {
		choices[i] = ui.Choice{Label: orgLabel(o.Name, o.Slug), Value: o.ID}
	}
	if defID == "" {
		defID = orgctx.ChooseDefault(orgIDs(orgs), "")
	}
	id, err := ui.PickOne("Select organization", choices, defID)
	if err != nil {
		return driverforgeapi.Organization{}, err
	}
	org := findByID(orgs, id)
	if org == nil {
		return driverforgeapi.Organization{}, fmt.Errorf("no organization selected")
	}
	return *org, nil
}

// applyOrg persists the org as current and prints confirmation.
func applyOrg(out io.Writer, o driverforgeapi.Organization) error {
	if err := orgctx.SetCurrent(orgctx.Org{ID: o.ID, Name: o.Name, Slug: o.Slug}); err != nil {
		return fmt.Errorf("saving organization: %w", err)
	}
	fmt.Fprintf(out, "Using organization: %s\n", orgLabel(o.Name, o.Slug))
	return nil
}

// findOrg matches an arg against slug, UUID id, then exact name (case-insensitive
// for slug/name).
func findOrg(orgs []driverforgeapi.Organization, arg string) *driverforgeapi.Organization {
	lower := strings.ToLower(arg)
	for i := range orgs {
		if strings.ToLower(orgs[i].Slug) == lower {
			return &orgs[i]
		}
	}
	for i := range orgs {
		if orgs[i].ID == arg {
			return &orgs[i]
		}
	}
	for i := range orgs {
		if strings.EqualFold(orgs[i].Name, arg) {
			return &orgs[i]
		}
	}
	return nil
}

func findByID(orgs []driverforgeapi.Organization, id string) *driverforgeapi.Organization {
	for i := range orgs {
		if orgs[i].ID == id {
			return &orgs[i]
		}
	}
	return nil
}

func orgIDs(orgs []driverforgeapi.Organization) []string {
	ids := make([]string, len(orgs))
	for i, o := range orgs {
		ids[i] = o.ID
	}
	return ids
}

func orgLabel(name, slug string) string {
	if slug != "" {
		return fmt.Sprintf("%s (%s)", name, slug)
	}
	return name
}
