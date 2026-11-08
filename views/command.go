package views

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ivy/almanac/diag"
	"github.com/ivy/almanac/model"
	"github.com/ivy/almanac/vocab"
)

// Commands are the verbs that draw an almanac's workflows, for its binary to add to its root
// command: "outcomes" lists every way a run can end, and "views" writes a page. Each takes a
// workflow's name, which may be left out when the almanac has only one, and --diff, a file
// holding what "outcomes --format json" printed for an older version.
func Commands(v *vocab.Vocabulary, workflows ...model.Workflow) []*cobra.Command {
	var format, outcomesOld string
	outcomes := &cobra.Command{
		Use:   "outcomes [WORKFLOW]",
		Short: "List every way a workflow's run can end, with its conditions and effects",
		Long: `outcomes lists every way a run of WORKFLOW can end: the conditions a run meets on
the way, and the effects it produces there. It reads only the model and its
vocabulary, never a run.

With --diff OLD.json, where OLD.json is what "outcomes --format json" printed for
another version, it lists the endings added, removed and changed instead.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := pick(workflows, args)
			if err != nil {
				return err
			}
			s, err := Outcomes(v, w)
			if err != nil {
				return err
			}
			if format != "json" && format != "text" {
				return &diag.Error{Code: diag.Usage, Message: fmt.Sprintf("--format %q is not a format", format), Help: "pass --format text or --format json"}
			}
			out := cmd.OutOrStdout()
			if outcomesOld != "" {
				old, err := readSummary(outcomesOld)
				if err != nil {
					return err
				}
				c := Compare(old, s)
				if format == "json" {
					return writeJSON(out, c)
				}
				return writeChanges(out, c)
			}
			if format == "json" {
				return writeJSON(out, s)
			}
			return writeOutcomes(out, s)
		},
	}
	outcomes.Flags().StringVar(&format, "format", "text", "text or json")
	outcomes.Flags().StringVar(&outcomesOld, "diff", "", "compare with an older version's `OLD.json`, from outcomes --format json")

	var viewsOld string
	page := &cobra.Command{
		Use:   "views [WORKFLOW]",
		Short: "Write a workflow's flow, outcomes and hostile facts as one HTML page",
		Long: `views writes one self-contained HTML page for WORKFLOW to standard output: its
flow, every way a run can end, and where hostile facts go. The page has no
script and loads nothing, and it holds only names and labels from the model and
its vocabulary, never anything a run read.

With --diff OLD.json, where OLD.json is what "outcomes --format json" printed for
another version, the page also lists what changed and marks it on the flow.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			w, err := pick(workflows, args)
			if err != nil {
				return err
			}
			var opts Options
			if viewsOld != "" {
				old, err := readSummary(viewsOld)
				if err != nil {
					return err
				}
				opts.Old = &old
			}
			html, err := Page(v, w, opts)
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(html)
			return err
		},
	}
	page.Flags().StringVar(&viewsOld, "diff", "", "compare with an older version's `OLD.json`, from outcomes --format json")
	return []*cobra.Command{outcomes, page}
}

// pick is the workflow args name, or the only one.
func pick(workflows []model.Workflow, args []string) (model.Workflow, error) {
	names := make([]string, len(workflows))
	for i, w := range workflows {
		names[i] = w.Name
	}
	if len(args) == 0 {
		if len(workflows) == 1 {
			return workflows[0], nil
		}
		return model.Workflow{}, &diag.Error{Code: diag.Usage, Message: "no workflow named, and this almanac has several", Help: "name one of: " + strings.Join(names, ", ")}
	}
	for _, w := range workflows {
		if w.Name == args[0] {
			return w, nil
		}
	}
	return model.Workflow{}, &diag.Error{Code: diag.Usage, Message: fmt.Sprintf("no workflow named %q in this almanac", args[0]), Help: "name one of: " + strings.Join(names, ", ")}
}

func readSummary(path string) (Summary, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Summary{}, diag.UnreadableInput(path, err)
	}
	var s Summary
	if err := json.Unmarshal(raw, &s); err != nil || s.Workflow == "" {
		if err == nil {
			err = fmt.Errorf("it names no workflow")
		}
		return Summary{}, &diag.Error{
			Code:    diag.InputInvalidJSON,
			Message: path + " is not a workflow's outcomes, so there is nothing to compare with",
			Help:    `write it with "outcomes WORKFLOW --format json" from the binary of the older version`,
			Cause:   err,
		}
	}
	return s, nil
}

func writeJSON(out io.Writer, value any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(value)
}

func writeOutcomes(out io.Writer, s Summary) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s\n%d ways to end\n", s.Workflow, s.Intent, len(s.Endings))
	for _, o := range s.Endings {
		fmt.Fprintf(&b, "\n%s  %s\n", o.ID(), o.Label)
		if o.When != nil {
			fmt.Fprintf(&b, "  ends %s\n", o.When)
		} else {
			fmt.Fprintf(&b, "  ends when no earlier rule matched\n")
		}
		for _, t := range o.Path {
			fmt.Fprintf(&b, "    after: %s\n", t)
		}
		for _, e := range o.Along {
			fmt.Fprintf(&b, "  earlier: %s\n", e.Text)
		}
		for _, e := range o.Effects {
			fmt.Fprintf(&b, "  → %s\n", e.Text)
		}
		if len(o.Effects) == 0 {
			fmt.Fprintf(&b, "  → no effects\n")
		}
	}
	_, err := io.WriteString(out, b.String())
	return err
}

func writeChanges(out io.Writer, c Changes) error {
	var b strings.Builder
	if c.Empty() {
		b.WriteString("no changes\n")
	}
	for _, s := range c.Settings {
		fmt.Fprintf(&b, "setting %s: %s → %s\n", s.Name, s.Old, s.New)
	}
	for _, s := range c.Steps {
		fmt.Fprintf(&b, "step %s %s: %s\n", s.Change, s.ID, s.Label)
		for _, n := range s.Notes {
			fmt.Fprintf(&b, "  %s\n", n)
		}
	}
	for _, o := range c.Added {
		fmt.Fprintf(&b, "added %s: %s\n", o.ID(), o.Label)
	}
	for _, o := range c.Removed {
		fmt.Fprintf(&b, "removed %s: %s\n", o.ID(), o.Label)
	}
	for _, ch := range c.Changed {
		fmt.Fprintf(&b, "changed %s: %s\n", ch.Ending, ch.Label)
		for _, n := range ch.Notes {
			fmt.Fprintf(&b, "  %s\n", n)
		}
	}
	for _, ch := range c.PathOnly {
		fmt.Fprintf(&b, "reached another way %s: %s\n", ch.Ending, ch.Label)
		for _, n := range ch.Notes {
			fmt.Fprintf(&b, "  %s\n", n)
		}
	}
	_, err := io.WriteString(out, b.String())
	return err
}
