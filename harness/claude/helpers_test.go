package claude_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/agent"
	"github.com/ivy/almanac/cooled"
	"github.com/ivy/almanac/harness/claude"
	"github.com/ivy/almanac/hostile/reveal"
)

// Sessions are reached the way a run reaches them: through an agent's call,
// which the harness insists on.

func mustPrompt(text string) agent.Prompt {
	prompt, err := agent.ParsePrompt("spec", text)
	Expect(err).NotTo(HaveOccurred())
	return prompt
}

// read runs a Quarantined session: no write, no egress, no secrets.
func read(ctx context.Context, h agent.Harness, prompt string, input any) (json.RawMessage, error) {
	call, err := agent.NewQuarantined("reviewer", mustPrompt(prompt)).Read(nil, input)
	Expect(err).NotTo(HaveOccurred())
	answer, err := call.Run(ctx, h)
	return reveal.Value(answer), err
}

// repair runs a Fixer session, which may write in a sandbox.
func repair(ctx context.Context, h agent.Harness, prompt string, input any) (json.RawMessage, error) {
	now := time.Now()
	_, proof := cooled.Measure(cooled.PublishedAt(now.Add(-30*24*time.Hour)), 24*time.Hour, now)
	call, err := agent.NewFixer("fixer", mustPrompt(prompt)).Repair(nil, input, proof)
	Expect(err).NotTo(HaveOccurred())
	answer, err := call.Run(ctx, h)
	return reveal.Value(answer), err
}

// worker runs a Worker session, which has egress and secrets.
func worker(ctx context.Context, h agent.Harness) error {
	call, err := agent.NewWorker("worker", mustPrompt("Act.")).Act(nil, nil)
	Expect(err).NotTo(HaveOccurred())
	_, err = call.Run(ctx, h)
	return err
}

func newHarness(config claude.Config) *claude.Harness {
	h, err := claude.New(config)
	Expect(err).NotTo(HaveOccurred())
	return h
}

// fakeCLI is a shell script standing in for the CLI. It saves its stdin to
// dir/stdin and its environment to dir/env, then runs body.
func fakeCLI(dir, body string) claude.Config {
	path := filepath.Join(dir, "claude")
	script := fmt.Sprintf("#!/bin/sh\ncat > %s\nenv > %s\n%s\n",
		filepath.Join(dir, "stdin"), filepath.Join(dir, "env"), body)
	Expect(os.WriteFile(path, []byte(script), 0o755)).To(Succeed())
	config := claude.DefaultConfig(claude.APIKey("key"))
	config.Binary = path
	return config
}

// answers is a fake CLI's body that ends a session with text.
func answers(text string) string {
	return "echo '" + jsonLine(map[string]any{"type": "result", "is_error": false, "result": text}) + "'"
}

func jsonLine(value any) string {
	encoded, err := json.Marshal(value)
	Expect(err).NotTo(HaveOccurred())
	return string(encoded)
}
