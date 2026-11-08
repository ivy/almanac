package check

import (
	"slices"

	"github.com/ivy/almanac/agent"
	"github.com/ivy/almanac/model"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/vocab"
	"github.com/ivy/almanac/workflow"
)

// Manifest is w's reviewable declaration (R-43): its trigger, budgets and effects, the agents
// it calls with their grants and prompts, and every provider the parts it uses read through. It
// is the manifest the engine reports for w, so it must be made from a model Check accepted.
func Manifest(v *vocab.Vocabulary, w model.Workflow) (workflow.Manifest, error) {
	digest, err := model.Digest(w)
	if err != nil {
		return workflow.Manifest{}, err
	}
	m := workflow.Manifest{
		Workflow:    w.Name,
		Intent:      w.Intent,
		On:          w.On,
		Scans:       w.Scan != "",
		RunsPerHour: w.Limits.RunsPerHour,
		Attempts:    w.Limits.Attempts,
		May:         slices.Clone(w.May),
		Agents:      []agent.Info{},
		Requires:    []providers.Requirement{},
		Digest:      digest,
	}
	need := func(entry *vocab.Entry) {
		for _, n := range entry.Needs {
			if !slices.Contains(m.Requires, n) {
				m.Requires = append(m.Requires, n)
			}
		}
	}
	if event, ok := v.Lookup(w.On); ok && len(event.Kinds) > 0 {
		m.On = event.Kinds[0]
	}
	if scan, ok := v.Lookup(w.Scan); ok {
		need(scan)
	}
	for _, s := range w.Flow {
		var used string
		switch {
		case s.Observe != nil:
			used = s.Observe.From
		case s.Measure != nil:
			used = s.Measure.From
		case s.Screen != nil:
			if classifier := providers.Require[agent.Classifier](); !slices.Contains(m.Requires, classifier) {
				m.Requires = append(m.Requires, classifier)
			}
		case s.Agent != nil:
			if entry, ok := v.Lookup(s.Agent.Agent); ok && entry.Agent != nil &&
				!slices.ContainsFunc(m.Agents, func(a agent.Info) bool { return a.Name == entry.Name }) {
				m.Agents = append(m.Agents, *entry.Agent)
			}
		}
		if entry, ok := v.Lookup(used); ok {
			need(entry)
		}
	}
	return m, nil
}
