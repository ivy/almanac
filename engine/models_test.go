package engine_test

// The models the specs build, written with the DSL. They are in a file of their own because the
// DSL's When, Not, Measure and Pending share names with Ginkgo's and Gomega's, and a dot import
// reaches only its own file.

import (
	"context"
	"encoding/json"

	"github.com/ivy/almanac/agent"
	. "github.com/ivy/almanac/dsl"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/model"
	"github.com/ivy/almanac/providers"
	"github.com/ivy/almanac/vocab"
	"github.com/ivy/almanac/wire"
	"github.com/ivy/almanac/workflow"
)

// probes is a vocabulary of parts that test the engine's edges.
var probes = vocab.New()

// probeEvent is something happening in a repository.
type probeEvent struct {
	Repo hostile.Slug `json:"repo"`
	Stop bool         `json:"stop"`
}

func (probeEvent) Kind() string        { return "probe.happened" }
func (probeEvent) AlsoKinds() []string { return []string{"probe.happened_again"} }

func (e *probeEvent) Parse(p *wire.Payload) {
	e.Repo = wire.Field[hostile.Slug](p, "/repo")
	e.Stop = wire.Field[bool](p, "/stop")
}

// reading is what a probe observer reads.
type reading struct {
	Seen    bool         `json:"seen"`
	Package hostile.Slug `json:"package"`
}

// Packages says which package an event is about.
type Packages interface {
	Package(ctx context.Context, repo hostile.Slug) (hostile.Slug, error)
}

var (
	probeHappened = vocab.Event[probeEvent](probes, "probe.happened", "something happened")
	stops         = vocab.Predicate(probes, "probe.stops", "the event says to stop", stopsHere)
	readsForge    = vocab.Observer(probes, "probe.reads_forge", "reads through a forge it never declared", readForge)
	packageOf     = vocab.Observer(probes, "probe.package", "the package the event is about", readPackage, providers.Require[Packages]())
	probeScan     = vocab.Scan(probes, "probe.scan", "everything that happened", scanOne)
	watcher       = vocab.Reader[reading](probes, "watches what happened",
		agent.NewQuarantined("watcher", agent.MustPrompt(agent.ParsePrompt("watcher.md", "Watch this."))))
)

func stopsHere(e probeEvent) bool { return e.Stop }

func readForge(ctx context.Context, from workflow.Lookup, e probeEvent) (reading, error) {
	forge, err := workflow.Get[effect.Forge](from)
	if err != nil {
		return reading{}, err
	}
	return reading{Seen: true}, forge.Attest(ctx, "probe", effect.Attest{Context: "probe", State: effect.Success, Repo: e.Repo})
}

func readPackage(ctx context.Context, from workflow.Lookup, e probeEvent) (reading, error) {
	packages, err := workflow.Get[Packages](from)
	if err != nil {
		return reading{}, err
	}
	pkg, err := packages.Package(ctx, e.Repo)
	return reading{Seen: true, Package: pkg}, err
}

func scanOne(context.Context, *workflow.Scanner) ([]workflow.Found, error) {
	return []workflow.Found{{DeliveryID: "scan:1", Payload: json.RawMessage(`{"repo": "octo-org/hello", "stop": false}`)}}, nil
}

var (
	happened     = EventOf(probeHappened)
	happenedRepo = Field[hostile.Slug](happened, "repo")
)

// probe is a workflow on probeHappened with steps.
func probe(steps ...Part) model.Workflow {
	return Define("probe", append([]Part{
		Intent("Probe the engine."),
		On(probeHappened),
		Key(happenedRepo),
		Subjects(RepoSubject(happenedRepo)),
		May(Awaits),
		Limits(PerHour(1)),
	}, steps...)...)
}

// sneaky observes, as fact, through an observer that reaches for a forge it never declared.
func sneaky(fact string) model.Workflow {
	return probe(
		Observe("read", "Read", Fact[reading](fact), readsForge, happened),
		Decide("done", "Done", Otherwise("end", "Nothing more", Await("nothing"))),
	)
}

// linked links the package it reads, unless the event says to stop before reading it.
var linked = func() model.Workflow {
	pkg := Fact[reading]("package")
	return probe(
		Links(PackageSubject(happenedRepo, Field[hostile.Slug](pkg, "package"))),
		Scan(probeScan),
		When(Not(happened.Is(stops))),
		Decide("early", "Stop early?", Rule("stop", "The event says stop", happened.Is(stops), Await("the next event"))),
		Observe("package", "Read the package", pkg, packageOf, happened),
		Decide("done", "Done", Otherwise("end", "Nothing more", Await("nothing"))),
	)
}()

// sharesAnAgentsName observes a fact named like the agent it calls next.
var sharesAnAgentsName = probe(
	Observe("read", "Read", Fact[reading]("watcher"), packageOf, happened),
	Read("watch", "Watch", Hostile[reading]("watched"), watcher),
	Decide("done", "Done", Otherwise("end", "Nothing more", Await("nothing"))),
)
