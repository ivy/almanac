// Command almanac-views draws the Renovate almanac's workflows: "outcomes" lists every way a
// run can end, and "views" writes a page. The verbs are package views' own, so the almanac's
// binary can add them beside its other verbs.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ivy/almanac/almanacs/renovate"
	"github.com/ivy/almanac/almanacs/renovate/buildfailures"
	"github.com/ivy/almanac/almanacs/renovate/securityfixes"
	"github.com/ivy/almanac/diag"
	"github.com/ivy/almanac/views"
)

func main() {
	if err := command().Execute(); err != nil {
		var d *diag.Error
		if errors.As(err, &d) {
			fmt.Fprint(os.Stderr, diag.Render(d, diag.Human))
		} else {
			fmt.Fprintf(os.Stderr, "almanac-views: %v\n", err)
		}
		os.Exit(1)
	}
}

func command() *cobra.Command {
	root := &cobra.Command{
		Use:           "almanac-views",
		Short:         "Draw the Renovate almanac's workflows from their models",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(views.Commands(renovate.Vocabulary, buildfailures.Model, securityfixes.Model)...)
	return root
}
