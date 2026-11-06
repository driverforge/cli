package cli

import (
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// helpOut is a command's help in structured output: what a person reads in
// --help, as fields a program can rely on.
type helpOut struct {
	Command     string       `json:"command"`
	Short       string       `json:"short"`
	Long        string       `json:"long"`
	Usage       string       `json:"usage"`
	Aliases     []string     `json:"aliases"`
	Examples    []string     `json:"examples"`
	Flags       []helpFlag   `json:"flags"`
	GlobalFlags []helpFlag   `json:"global_flags"`
	Subcommands []helpSubcmd `json:"subcommands"`
}

type helpFlag struct {
	Name      string  `json:"name"`
	Shorthand *string `json:"shorthand"`
	Type      string  `json:"type"`
	Default   string  `json:"default"`
	Usage     string  `json:"usage"`
}

type helpSubcmd struct {
	Name  string `json:"name"`
	Short string `json:"short"`
}

// installStructuredHelp makes --help, `driverforge help <command>` and a bare
// command group write one JSON document under structured output, as every
// other command does. A person still gets cobra's text help.
func installStructuredHelp(root *cobra.Command) {
	text := root.HelpFunc()
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		if !structured() {
			text(cmd, args)
			return
		}
		writeJSON(cmd.OutOrStdout(), helpDoc(cmd))
	})
}

func helpDoc(cmd *cobra.Command) helpOut {
	doc := helpOut{
		Command:     cmd.CommandPath(),
		Short:       cmd.Short,
		Long:        strings.TrimSpace(cmd.Long),
		Usage:       cmd.UseLine(),
		Aliases:     append([]string{}, cmd.Aliases...),
		Examples:    []string{},
		Flags:       helpFlags(cmd.LocalFlags()),
		GlobalFlags: helpFlags(cmd.InheritedFlags()),
		Subcommands: []helpSubcmd{},
	}
	for _, line := range strings.Split(cmd.Example, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			doc.Examples = append(doc.Examples, line)
		}
	}
	for _, c := range cmd.Commands() {
		if c.IsAvailableCommand() {
			doc.Subcommands = append(doc.Subcommands, helpSubcmd{Name: c.Name(), Short: c.Short})
		}
	}
	return doc
}

func helpFlags(fs *pflag.FlagSet) []helpFlag {
	out := []helpFlag{}
	fs.VisitAll(func(f *pflag.Flag) {
		if f.Hidden || f.Name == "help" {
			return
		}
		out = append(out, helpFlag{
			Name: f.Name, Shorthand: optional(f.Shorthand), Type: f.Value.Type(),
			Default: f.DefValue, Usage: f.Usage,
		})
	})
	return out
}
