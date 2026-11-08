package renovate

import (
	"embed"
	"io/fs"

	"github.com/ivy/almanac/agent"
	"github.com/ivy/almanac/patch"
	"github.com/ivy/almanac/vocab"
)

// embedded holds the agents' instructions, shared with the Rust almanac until the cutover.
//
//go:embed prompts/fixer.md prompts/reviewer.md
var embedded embed.FS

// prompts names each prompt by its file, as the manifest shows it.
var prompts = must(fs.Sub(embedded, "prompts"))

func must(fsys fs.FS, err error) fs.FS {
	if err != nil {
		panic(err)
	}
	return fsys
}

// The agents: a fixer that repairs a build in a sandbox once the update has cooled, and a
// reviewer that reads a security update's diff and can only say no.
var (
	Fixer = vocab.Fixer[patch.Patch](Vocabulary,
		"repairs a broken build in a sandbox holding the pull request's head, from its failure logs",
		agent.NewFixer("fixer", agent.MustPrompt(agent.LoadPrompt(prompts, "fixer.md", "repo"))))
	Reviewer = vocab.Reader[Review](Vocabulary,
		"reads the diff of a security update's releases and says whether it raises concerns",
		agent.NewQuarantined("reviewer", agent.MustPrompt(agent.LoadPrompt(prompts, "reviewer.md", "repo"))))
)
