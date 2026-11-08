// Package app is what an almanac binary's main calls: the verbs every almanac has, the daemon
// behind run, and the consumer that reads the broker's deliveries log into a runtime. An almanac
// gives it a name, its workflow models and the vocabulary they are written in, how to set up
// its providers, and any verbs of its own.
package app

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/ivy/almanac/config"
	"github.com/ivy/almanac/diag"
	"github.com/ivy/almanac/engine"
	"github.com/ivy/almanac/model"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/runtime"
	"github.com/ivy/almanac/vocab"
	"github.com/ivy/almanac/workflow"
)

// Almanac is one almanac binary.
type Almanac struct {
	// Name names the binary, such as almanac-renovate. It is the scope its rows are stored
	// under and the name of its cursor in the deliveries log, with ".dry-run" after it in a
	// dry run.
	Name string
	// Vocabulary is what Models are written in.
	Vocabulary *vocab.Vocabulary
	// Models are the almanac's workflows. Each is checked and made runnable when the binary
	// starts, and a binary with a model the checker refuses does not start.
	Models []model.Workflow
	// Workflows are workflows written as code, such as workflow.Code values in tests. A
	// delivery goes to the first workflow that handles its kind, models first, then these, in
	// order.
	Workflows []workflow.Registered
	// Limits are the limits the binary declares. Config can only tighten them. The zero value
	// declares config.DefaultLimits.
	Limits config.Limits
	// Setup makes the providers the workflows need. A provider it cannot make is best left
	// out, so that check and every verb name what is missing. Nil provides nothing.
	Setup func(ctx context.Context, s Setting) (*providers.Set, error)
	// Verbs are the almanac's own verbs, beside the ones every almanac has.
	Verbs []Verb
	// Commands makes cobra commands the almanac adds whole, writing their own output, such as
	// the views package's outcomes and views, whose --format is their own. A verb that says
	// what it has to say through Said belongs in Verbs. It is called once per invocation, and
	// the usage line and help list what it makes after Verbs. An error a command returns is
	// printed the way every verb prints one. Nil adds none.
	Commands func() []*cobra.Command

	// grace is how long an in-flight run gets once run is asked to stop.
	grace time.Duration
}

// Setting is what one invocation of a binary runs with.
type Setting struct {
	// Name is the almanac's name.
	Name string
	// Config is what the operator set in almanac.toml and ALMANAC_* variables.
	Config config.Config
	// Switches say whether effects apply, and whether any write or model call may be made at
	// all. A provider that writes through a concrete type, rather than an interface the
	// runtime can wrap, must refuse writes itself when Switches.ReadOnly is set.
	Switches runtime.Switches
	// Flags are the flags an almanac's own verb declared, parsed from its command line. It is
	// nil for a verb that declares none.
	Flags *pflag.FlagSet
}

// Verb is a verb an almanac adds of its own, such as report.
type Verb struct {
	// Name is what a person types, such as "report".
	Name string
	// Args describes the arguments after the name, for the usage line. Empty takes none.
	Args string
	// Short says what the verb does, in a sentence.
	Short string
	// Run does the verb with the arguments after its name. What it says is printed for a
	// person, or as JSON with --format json. An error is printed the way every verb prints
	// one, and the binary exits 1.
	Run func(ctx context.Context, s Setting, args []string) (Said, error)
	// Flags declares the verb's flags, such as --since, on its flag set, and Run reads them
	// from Setting.Flags. Nil declares none, and a flag on the command line is then an error.
	Flags func(flags *pflag.FlagSet)
}

// Said is what a verb has to say, for people and for programs.
type Said struct {
	// Human is printed as it is, with a newline after it.
	Human string
	// JSON is printed as one JSON value.
	JSON any
}

// Main runs the binary on its command line and exits: 0 when the verb succeeded, and 1 on
// an error. SIGTERM or SIGINT asks a running verb to stop.
func (a Almanac) Main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := a.Execute(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// Execute runs one command line, args being everything after the binary's name, and returns
// the exit status. Output goes to stdout and errors to stderr, in the format the command line
// asks for. Ending ctx asks the verb to stop, as SIGTERM does.
func (a Almanac) Execute(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	out := &output{name: a.Name, format: formatOf(args), stdout: stdout, stderr: stderr}
	root := a.command(out, args)
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	if err := root.ExecuteContext(ctx); err != nil {
		out.fail(err)
		return 1
	}
	return 0
}

// workflows are the almanac's workflows, made runnable: models first, then workflows written
// as code. Every problem with every model is reported together.
func (a *Almanac) workflows() ([]workflow.Registered, error) {
	all := make([]workflow.Registered, 0, len(a.Models)+len(a.Workflows))
	var problems diag.Problems
	for _, m := range a.Models {
		w, err := engine.New(a.Vocabulary, m)
		if err != nil {
			problems = append(problems, unwrapProblems(err)...)
			continue
		}
		all = append(all, w)
	}
	if len(problems) > 0 {
		return nil, problems
	}
	return append(all, a.Workflows...), nil
}

// commands are the commands the almanac adds whole, made for one invocation.
func (a *Almanac) commands() []*cobra.Command {
	if a.Commands == nil {
		return nil
	}
	return a.Commands()
}

// limits are the limits the binary declares.
func (a *Almanac) limits() config.Limits {
	if a.Limits == (config.Limits{}) {
		return config.DefaultLimits()
	}
	return a.Limits
}

// providers are what Setup makes for s.
func (a *Almanac) providers(ctx context.Context, s Setting) (*providers.Set, error) {
	if a.Setup == nil {
		return providers.New(), nil
	}
	set, err := a.Setup(ctx, s)
	if err != nil {
		return nil, err
	}
	if set == nil {
		return providers.New(), nil
	}
	return set, nil
}

// graceOrDefault is how long an in-flight run gets once run is asked to stop.
func (a *Almanac) graceOrDefault() time.Duration {
	if a.grace > 0 {
		return a.grace
	}
	return DefaultGrace
}
