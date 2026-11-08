package app

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ivy/almanac/diag"
)

// The verbs every almanac has, in the order the usage line lists them.
var builtins = []struct{ use, short string }{
	{"synth", "Print the manifest: each workflow's trigger, effects, budgets, agents and providers. Needs nothing set up."},
	{"check", "Confirm that every provider a workflow needs is set up, and print the mode and limits."},
	{"run [--dry-run]", "Run as a daemon: follow the deliveries log, scan on a schedule, and answer health checks."},
	{"backfill [--repo OWNER/NAME] [--dry-run]", "Run each workflow's scan once, then run what it found."},
	{"simulate KIND FILE [--dry-run]", "Send a JSON file through the production path as an event of that kind."},
	{"explain CODE", "Print the long form of an error."},
}

// usage is the one-line usage of the binary, its own verbs included.
func (a *Almanac) usage() string {
	verbs := make([]string, 0, len(builtins)+len(a.Verbs))
	for _, b := range builtins {
		verbs = append(verbs, b.use)
	}
	for _, v := range a.Verbs {
		verbs = append(verbs, strings.TrimSpace(v.Name+" "+v.Args))
	}
	for _, c := range a.commands() {
		verbs = append(verbs, c.Use)
	}
	return fmt.Sprintf("usage: %s [--format json] %s", a.Name, strings.Join(verbs, " | "))
}

// command is the command line of one invocation. Every error it returns is a *diag.Error, or
// one Setup or a verb returned.
func (a *Almanac) command(out *output, args []string) *cobra.Command {
	given := strings.Join(withoutFormat(args), " ")
	notACommand := func(cause error) error {
		e := diag.NotACommand(given, a.usage())
		e.Cause = cause
		return e
	}
	var format string
	root := &cobra.Command{
		Use:           a.Name,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ArbitraryArgs,
		PersistentPreRunE: func(*cobra.Command, []string) error {
			if format != "human" && format != "json" {
				return notACommand(fmt.Errorf("--format is human or json, not %q", format))
			}
			return nil
		},
		RunE: func(*cobra.Command, []string) error { return notACommand(nil) },
	}
	root.PersistentFlags().StringVar(&format, "format", "human", "human, or json for output a program can read")
	root.CompletionOptions.DisableDefaultCmd = true
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return notACommand(err) })
	root.SetHelpFunc(func(cmd *cobra.Command, _ []string) { a.help(cmd, out) })

	takes := func(n int) cobra.PositionalArgs {
		return func(_ *cobra.Command, args []string) error {
			if len(args) != n {
				return notACommand(nil)
			}
			return nil
		}
	}
	v := &verbs{a: a, out: out}
	root.AddCommand(
		&cobra.Command{Use: "synth", Short: builtins[0].short, Args: takes(0), RunE: v.synth},
		&cobra.Command{Use: "check", Short: builtins[1].short, Args: takes(0), RunE: v.check},
		withDryRun(&cobra.Command{Use: "run", Short: builtins[2].short, Args: takes(0), RunE: v.run}, &v.dryRun),
		withRepo(withDryRun(&cobra.Command{Use: "backfill", Short: builtins[3].short, Args: takes(0), RunE: v.backfill}, &v.dryRun), &v.repo),
		withDryRun(&cobra.Command{Use: "simulate KIND FILE", Short: builtins[4].short, Args: takes(2), RunE: v.simulate}, &v.dryRun),
		&cobra.Command{Use: "explain CODE", Short: builtins[5].short, Args: takes(1), RunE: v.explain},
	)
	for _, verb := range a.Verbs {
		cmd := &cobra.Command{
			Use:   strings.TrimSpace(verb.Name + " " + verb.Args),
			Short: verb.Short,
			Args:  cobra.ArbitraryArgs,
			RunE:  v.own(verb),
		}
		if verb.Flags != nil {
			verb.Flags(cmd.Flags())
		}
		root.AddCommand(cmd)
	}
	root.AddCommand(a.commands()...)
	return root
}

// withoutFormat is args without --format and its value, which is not what went wrong.
func withoutFormat(args []string) []string {
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--format":
			i++
		case strings.HasPrefix(args[i], "--format="):
		default:
			rest = append(rest, args[i])
		}
	}
	return rest
}

func withDryRun(cmd *cobra.Command, dryRun *bool) *cobra.Command {
	cmd.Flags().BoolVar(dryRun, "dry-run", false, "apply nothing, whatever ALMANAC_MODE says")
	return cmd
}

func withRepo(cmd *cobra.Command, repo *string) *cobra.Command {
	cmd.Flags().StringVar(repo, "repo", "", "scan only this repository, OWNER/NAME")
	return cmd
}

// help prints the usage line and every verb for the binary, or one verb's usage and flags.
func (a *Almanac) help(cmd *cobra.Command, out *output) {
	var b strings.Builder
	if !cmd.HasParent() {
		fmt.Fprintf(&b, "%s\n\n", a.usage())
		for _, verb := range builtins {
			fmt.Fprintf(&b, "  %-42s %s\n", verb.use, verb.short)
		}
		for _, verb := range a.Verbs {
			fmt.Fprintf(&b, "  %-42s %s\n", strings.TrimSpace(verb.Name+" "+verb.Args), verb.Short)
		}
		for _, c := range a.commands() {
			fmt.Fprintf(&b, "  %-42s %s\n", c.Use, c.Short)
		}
		b.WriteString("\nPut --format json first for output a program can read. `explain CODE` prints the long form of any error.\n")
	} else {
		fmt.Fprintf(&b, "usage: %s %s\n\n%s\n", a.Name, cmd.Use, cmd.Short)
		if cmd.HasAvailableLocalFlags() {
			fmt.Fprintf(&b, "\nFlags:\n%s", cmd.LocalFlags().FlagUsages())
		}
	}
	_, _ = io.WriteString(out.stdout, b.String())
}
