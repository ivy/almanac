// Package check refuses a workflow model that breaks a rule, before any event arrives. It runs
// on every model when a binary starts and in a spec for every workflow, so `make go-check`
// fails on a model the checker refuses. Each problem is a *diag.Error whose code names the rule
// and whose message names the workflow, step and rule where it is broken.
//
// The rules: every name resolves in the vocabulary, with the types its signature takes; a fact
// is produced before anything reads it, and only once; an effect, or a hold, is of a kind the
// workflow may produce; a hostile fact goes only to a narrowing predicate, a map, an observer or
// an agent; a title holds only literals and clean values; a worker reads no hostile input; a
// prompt is filled exactly, from clean facts; a push takes only a patch a check_patch step
// accepted; a fixer comes after a measure, past a decide that ends every run whose release has
// not cooled; every step and rule has a unique ID and a label; nothing follows an otherwise
// rule; and the flow ends in a decide with an otherwise rule.
package check

import (
	"cmp"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/ivy/almanac/cooled"
	"github.com/ivy/almanac/diag"
	"github.com/ivy/almanac/effect"
	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/model"
	"github.com/ivy/almanac/patch"
	"github.com/ivy/almanac/vocab"
)

// Check is every problem with w against v, as diag.Problems, or nil if there is none. A
// vocabulary with problems of its own refuses every model.
func Check(v *vocab.Vocabulary, w model.Workflow) error {
	if err := v.Err(); err != nil {
		return err
	}
	c := &checker{
		v:        v,
		w:        w,
		facts:    map[string]reflect.Type{},
		declared: map[string]model.Fact{},
		ids:      map[string]bool{},
		checked:  map[string]bool{},
	}
	c.declarations()
	c.flow()
	if len(c.problems) == 0 {
		return nil
	}
	return c.problems
}

type checker struct {
	v        *vocab.Vocabulary
	w        model.Workflow
	problems diag.Problems
	// facts are the facts produced so far, by the Go type a part receives each as.
	facts map[string]reflect.Type
	// declared are the model's own declarations of its facts.
	declared map[string]model.Fact
	ids      map[string]bool
	// checked are the facts check_patch steps produced.
	checked  map[string]bool
	measured bool
	// ages are the ages a run can still have, by the name of the built-in predicate that
	// tests for each, once a measure produced one: a decide's rules that test the age alone
	// narrow them for the steps after it.
	ages map[string]bool
	// beforeRun is true while checking what is worked out before a run starts, from the
	// event alone.
	beforeRun bool
}

func (c *checker) refuse(err *diag.Error) { c.problems = append(c.problems, err) }

var (
	slugType    = reflect.TypeFor[hostile.Slug]()
	shaType     = reflect.TypeFor[hostile.Sha]()
	pullType    = reflect.TypeFor[uint64]()
	textType    = reflect.TypeFor[hostile.Of[string]]()
	patchType   = reflect.TypeFor[hostile.Of[patch.Patch]]()
	ageType     = reflect.TypeFor[cooled.Age]()
	triesType   = reflect.TypeFor[vocab.Attempts]()
	outcomeType = reflect.TypeFor[vocab.PatchOutcome]()
	workflowID  = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

// reserved are the facts the engine produces itself, which no observer or agent may name.
var reserved = []string{model.EventFact, model.AttemptsFact, model.AgeFact, model.CheckedFact}

func (c *checker) declarations() {
	c.beforeRun = true
	defer func() { c.beforeRun = false }()
	w, at := c.w, c.w.Name
	if !workflowID.MatchString(w.Name) {
		at = cmp.Or(w.Name, "(unnamed workflow)")
		c.refuse(diag.BadDeclaration(at, "a workflow's name is lowercase letters, digits and underscores, starting with a letter"))
	}
	if strings.TrimSpace(w.Intent) == "" {
		c.refuse(diag.BadDeclaration(at, "it has no intent, and review reads what a workflow is for from it"))
	}
	if w.Limits.RunsPerHour <= 0 {
		c.refuse(diag.BadDeclaration(at, "its runs per hour must be positive"))
	}
	if w.Limits.Attempts < 0 {
		c.refuse(diag.BadDeclaration(at, "its attempts limit cannot be negative"))
	}
	for i, kind := range w.May {
		if slices.Contains(w.May[:i], kind) || kind < effect.KindAttest || kind > effect.KindPush {
			c.refuse(diag.BadDeclaration(at, fmt.Sprintf("may lists %s twice, or a kind that is not an effect", kind)))
		}
	}
	if w.Scan != "" {
		c.entry(at, "scan", w.Scan, vocab.ScanKind)
	}
	for _, f := range w.Facts {
		if _, twice := c.declared[f.Name]; twice {
			c.refuse(diag.DuplicateFact(at, f.Name))
		}
		c.declared[f.Name] = f
	}
	if event, ok := c.entry(at, "event", w.On, vocab.EventKind); ok {
		c.produce(at, model.EventFact, event.Result)
	} else {
		c.facts[model.EventFact] = nil
	}
	if len(w.Key) == 0 {
		c.refuse(diag.BadDeclaration(at, "it has no key, so nothing would keep two runs of one subject apart"))
	}
	before := at + ", before a run"
	for _, ref := range w.Key {
		if t, ok := c.typeOf(before, ref, "a key"); ok && !keyable(t) {
			c.mismatch(before, ref, "a key", "slug, sha, number or bool", false, t)
		}
	}
	if w.When != nil {
		c.cond(before+", in when", *w.When)
	}
	c.subjects(before+", in subjects", w.Subjects)
}

// subjects checks subjects, each a repository or a package in one, made of slugs.
func (c *checker) subjects(at string, subjects []model.Subject) {
	for _, s := range subjects {
		parts := map[model.SubjectKind]int{model.RepoSubject: 1, model.PackageSubject: 2}[s.Kind]
		if parts == 0 || len(s.Of) != parts {
			c.refuse(diag.BadDeclaration(at, fmt.Sprintf("a %q subject is made of %d parts, and a subject is a repo of one slug or a package of two", s.Kind, len(s.Of))))
			continue
		}
		for _, ref := range s.Of {
			c.expect(at, ref, "part of a subject", slugType)
		}
	}
}

func (c *checker) flow() {
	w := c.w
	for i, s := range w.Flow {
		at := fmt.Sprintf("%s, step %q", w.Name, s.ID)
		c.identify(at, s.ID, s.Label)
		if !c.shaped(at, s) {
			continue
		}
		switch s.Kind {
		case model.Observe:
			c.observe(at, *s.Observe)
		case model.Screen:
			c.screen(at, *s.Screen)
		case model.Hold:
			c.hold(at, *s.Hold)
		case model.Measure:
			c.measure(at, *s.Measure)
		case model.Attempts:
			c.produce(at, model.AttemptsFact, triesType)
		case model.CheckPatch:
			c.checkPatch(at, *s.CheckPatch)
		case model.Agent:
			c.agent(at, *s.Agent)
		case model.Decide:
			c.decide(at, s, i == len(w.Flow)-1)
		}
	}
	if n := len(w.Flow); n == 0 || w.Flow[n-1].Kind != model.Decide {
		c.refuse(diag.NoEnding(w.Name, "the flow does not end with a decide, so a run could reach its end having decided nothing"))
	}
	c.subjects(w.Name+", in links", w.Links)
	for _, f := range w.Facts {
		if _, produced := c.facts[f.Name]; !produced {
			c.refuse(diag.BadDeclaration(w.Name, fmt.Sprintf("it declares the fact %q, and no step produces it", f.Name)))
		}
	}
}

// identify checks a step's or rule's ID and label.
func (c *checker) identify(at, id, label string) {
	switch {
	case id == "":
		c.refuse(diag.Unlabelled(at, "ID"))
	case c.ids[id]:
		c.refuse(diag.DuplicateID(at, id))
	}
	if strings.TrimSpace(label) == "" {
		c.refuse(diag.Unlabelled(at, "label"))
	}
	c.ids[id] = true
}

// shaped reports whether s holds exactly what its kind names.
func (c *checker) shaped(at string, s model.Step) bool {
	held := map[model.StepKind]bool{
		model.Observe:    s.Observe != nil,
		model.Screen:     s.Screen != nil,
		model.Hold:       s.Hold != nil,
		model.Measure:    s.Measure != nil,
		model.CheckPatch: s.CheckPatch != nil,
		model.Agent:      s.Agent != nil,
		model.Decide:     s.Decide != nil || s.Kind == model.Decide,
	}
	var holds []string
	for kind, set := range held {
		if set {
			holds = append(holds, string(kind))
		}
	}
	slices.Sort(holds)
	want := []string{string(s.Kind)}
	if s.Kind == model.Attempts {
		want = nil
	}
	if _, known := held[s.Kind]; !known && s.Kind != model.Attempts {
		c.refuse(diag.BadDeclaration(at, fmt.Sprintf("%q is not a kind of step", s.Kind)))
		return false
	}
	if !slices.Equal(holds, want) {
		c.refuse(diag.BadDeclaration(at, fmt.Sprintf("it is a %s step and holds %v", s.Kind, holds)))
		return false
	}
	return true
}

func (c *checker) observe(at string, o model.ObserveStep) {
	entry, ok := c.entry(at, "observer", o.From, vocab.ObserverKind)
	if !ok {
		c.unknown(o.Fact)
		return
	}
	c.args(at, entry, o.Args)
	c.produceNamed(at, o.Fact, entry.Result)
}

func (c *checker) screen(at string, s model.ScreenStep) {
	if strings.TrimSpace(s.Question) == "" {
		c.refuse(diag.BadDeclaration(at, "a screen asks no question"))
	}
	if s.Above <= 0 || s.Above >= 1 {
		c.refuse(diag.BadDeclaration(at, "a screen's threshold is a probability between 0 and 1"))
	}
	c.expect(at, s.Text, "the text a screen reads", textType)
}

func (c *checker) hold(at string, h model.HoldStep) {
	if !slices.Contains(c.w.May, effect.KindAttest) {
		c.refuse(diag.EffectNotAllowed(at, "holds a status, which is an Attest,"))
	}
	if strings.TrimSpace(h.Context) == "" {
		c.refuse(diag.BadDeclaration(at, "a hold names no status context"))
	}
	c.expect(at, h.Repo, "the repository of a hold", slugType)
	c.expect(at, h.Sha, "the commit of a hold", shaType)
}

func (c *checker) measure(at string, m model.MeasureStep) {
	if entry, ok := c.entry(at, "publish-time source", m.From, vocab.PublishedKind); ok {
		c.args(at, entry, m.Args)
	}
	if m.MinAge <= 0 {
		c.refuse(diag.BadDeclaration(at, "a measure's cooldown must be positive"))
	}
	c.produce(at, model.AgeFact, ageType)
	c.measured = true
	c.ages = map[string]bool{}
	for _, age := range agePredicates {
		c.ages[age] = true
	}
}

// agePredicates are the built-in predicates over the age, each testing for one of its values.
var agePredicates = []string{vocab.Cooled.Name(), vocab.Young.Name(), vocab.Unknown.Name()}

// pastRule narrows the ages a run can have once it goes on past a rule whose condition is
// cond, which was false for it. Only a condition that tests the age alone says anything.
func (c *checker) pastRule(cond model.Cond) {
	switch {
	case agePredicate(cond) != "":
		delete(c.ages, agePredicate(cond))
	case cond.Not != nil && agePredicate(*cond.Not) != "":
		only := agePredicate(*cond.Not)
		for age := range c.ages {
			if age != only {
				delete(c.ages, age)
			}
		}
	case cond.Any != nil:
		for _, each := range cond.Any {
			c.pastRule(each)
		}
	case len(cond.All) == 1:
		c.pastRule(cond.All[0])
	}
}

// agePredicate is the built-in age predicate cond tests the age with, or "".
func agePredicate(cond model.Cond) string {
	if slices.Contains(agePredicates, cond.Pred) && slices.Equal(cond.Args, []model.Ref{model.AgeFact}) {
		return cond.Pred
	}
	return ""
}

// uncooled are the ages other than cooled a run reaching here can still have, as words.
func (c *checker) uncooled() []string {
	var ages []string
	for _, age := range agePredicates[1:] {
		if c.ages[age] {
			ages = append(ages, strings.TrimPrefix(age, "age."))
		}
	}
	return ages
}

func (c *checker) checkPatch(at string, p model.CheckPatchStep) {
	c.expect(at, p.Patch, "the patch to check", patchType)
	c.expect(at, p.Base, "the commit a patch was made against", shaType)
	c.entry(at, "patch policy", p.Policy, vocab.PolicyKind)
	c.produce(at, model.CheckedFact, outcomeType)
	c.checked[model.CheckedFact] = true
}

func (c *checker) agent(at string, a model.AgentStep) {
	entry, ok := c.entry(at, "agent", a.Agent, vocab.AgentKind)
	if !ok {
		c.unknown(a.Fact)
		return
	}
	profile := entry.Agent.Profile
	switch {
	case profile == "fixer" && !c.measured:
		c.refuse(diag.FixerBeforeMeasure(at, a.Agent))
	case profile == "fixer" && len(c.uncooled()) > 0:
		c.refuse(diag.FixerUncooled(at, a.Agent, strings.Join(c.uncooled(), " or ")))
	}
	for _, name := range sortedKeys(a.Inputs) {
		t, ok := c.typeOf(at, a.Inputs[name], "an agent's input")
		if ok && profile == "worker" && hostile.CheckClean(t) != nil {
			c.refuse(diag.AgentInputHostile(at, a.Agent, name))
		}
	}
	wanted := entry.Agent.PromptVars
	for _, name := range wanted {
		if _, ok := a.Vars[name]; !ok {
			c.refuse(diag.PromptVars(at, a.Agent, fmt.Sprintf("the prompt's variable %q is not filled", name)))
		}
	}
	for _, name := range sortedKeys(a.Vars) {
		if !slices.Contains(wanted, name) {
			c.refuse(diag.PromptVars(at, a.Agent, fmt.Sprintf("%q is not a variable of the prompt", name)))
			continue
		}
		if t, ok := c.typeOf(at, a.Vars[name], "a prompt variable"); ok && hostile.CheckClean(t) != nil {
			c.refuse(diag.PromptVars(at, a.Agent, fmt.Sprintf("%q is filled from `%s`, which is not clean", name, a.Vars[name])))
		}
	}
	if a.Checkout != nil {
		c.expect(at, a.Checkout.Repo, "the repository to check out", slugType)
		c.expect(at, a.Checkout.Sha, "the commit to check out", shaType)
	}
	c.produceNamed(at, a.Fact, entry.Result)
}

func (c *checker) decide(at string, s model.Step, last bool) {
	if len(s.Decide) == 0 {
		c.refuse(diag.NoEnding(at, "the decide has no rules"))
		return
	}
	for i, rule := range s.Decide {
		ruleAt := fmt.Sprintf("%s, rule %q", at, rule.ID)
		c.identify(ruleAt, rule.ID, rule.Label)
		if rule.Otherwise() && i < len(s.Decide)-1 {
			c.refuse(diag.UnreachableStep(ruleAt, "it always matches, so the rules after it never run"))
		}
		if rule.When != nil {
			c.cond(ruleAt, *rule.When)
			c.pastRule(*rule.When)
		}
		for _, e := range rule.Then {
			c.effect(ruleAt, e)
		}
	}
	otherwise := s.Decide[len(s.Decide)-1].Otherwise()
	if otherwise && !last {
		c.refuse(diag.UnreachableStep(at, "it ends with an otherwise rule, so the steps after it never run"))
	}
	if last && !otherwise {
		c.refuse(diag.NoEnding(at, "the last decide has no otherwise rule, so a run that matches none of its rules ends having decided nothing"))
	}
}

func (c *checker) cond(at string, cond model.Cond) {
	set := 0
	for _, isSet := range []bool{cond.Pred != "", cond.Not != nil, cond.All != nil, cond.Any != nil} {
		if isSet {
			set++
		}
	}
	if set != 1 {
		c.refuse(diag.BadDeclaration(at, "a condition is exactly one of a predicate, not, all or any"))
		return
	}
	switch {
	case cond.Not != nil:
		c.cond(at, *cond.Not)
	case cond.All != nil || cond.Any != nil:
		for _, each := range slices.Concat(cond.All, cond.Any) {
			c.cond(at, each)
		}
	default:
		c.predicate(at, cond)
	}
}

func (c *checker) predicate(at string, cond model.Cond) {
	entry, ok := c.entry(at, "predicate", cond.Pred, vocab.PredicateKind)
	if !ok {
		return
	}
	c.args(at, entry, cond.Args)
	switch {
	case entry.Values != nil && !slices.Contains(entry.Values, cond.Is):
		c.refuse(diag.UnknownName(at, "value of the classifier "+cond.Pred+" named", cond.Is))
	case entry.Values == nil && cond.Is != "":
		c.refuse(diag.UnknownName(at, "value of the yes-or-no predicate "+cond.Pred+" named", cond.Is))
	}
	if cond.Pred == vocab.Spent.Name() && c.w.Limits.Attempts == 0 {
		c.refuse(diag.BadDeclaration(at, "it tests whether the attempts are spent, and the workflow declares no attempts limit"))
	}
}

// args checks refs against the parameters of entry.
func (c *checker) args(at string, entry *vocab.Entry, refs []model.Ref) {
	if len(refs) != len(entry.Params) {
		c.refuse(diag.BadDeclaration(at, fmt.Sprintf("%s takes %d facts and is given %d", entry.Name, len(entry.Params), len(refs))))
		return
	}
	for i, ref := range refs {
		c.expect(at, ref, "an argument to "+entry.Name, entry.Params[i])
	}
}

func (c *checker) effect(at string, e model.Effect) {
	kind := e.Kind()
	if set(e) != 1 {
		c.refuse(diag.BadDeclaration(at, "an effect is exactly one of Attest, Await, File, Ask and Push"))
		return
	}
	if !slices.Contains(c.w.May, kind) {
		c.refuse(diag.EffectNotAllowed(at, "produces an effect of kind "+kind.String()+","))
	}
	switch {
	case e.Attest != nil:
		if e.Attest.Context == "" || e.Attest.State < effect.Pending || e.Attest.State > effect.Failure {
			c.refuse(diag.BadDeclaration(at, "an Attest names a status context and a state"))
		}
		c.expect(at, e.Attest.Repo, "the repository of an Attest", slugType)
		c.expect(at, e.Attest.Sha, "the commit of an Attest", shaType)
	case e.Await != nil:
		if strings.TrimSpace(e.Await.Until) == "" {
			c.refuse(diag.BadDeclaration(at, "an Await says what it waits for"))
		}
	case e.File != nil:
		c.expect(at, e.File.Target, "where a File goes", slugType)
		c.title(at, e.File.Title)
		c.body(at, e.File.Body)
	case e.Ask != nil:
		c.ask(at, *e.Ask)
	case e.Push != nil:
		c.push(at, *e.Push)
	}
}

func set(e model.Effect) int {
	n := 0
	for _, isSet := range []bool{e.Attest != nil, e.Await != nil, e.File != nil, e.Ask != nil, e.Push != nil} {
		if isSet {
			n++
		}
	}
	return n
}

func (c *checker) ask(at string, a model.AskEffect) {
	c.expect(at, a.Target, "where an Ask goes", slugType)
	if a.Pull != "" {
		c.expect(at, a.Pull, "the pull request an Ask is about", pullType)
	}
	c.title(at, a.Title)
	c.body(at, a.Body)
	if a.Once {
		c.typeOf(at, model.AttemptsFact, "whether a person was asked")
	}
	if a.ThenAwait != "" && !a.Once {
		c.refuse(diag.BadDeclaration(at, "an Ask awaits once a person was asked only if it asks once"))
	}
	if a.ThenAwait != "" && !slices.Contains(c.w.May, effect.KindAwait) {
		c.refuse(diag.EffectNotAllowed(at, "awaits once a person was asked, which is an Await,"))
	}
}

func (c *checker) push(at string, p model.PushEffect) {
	if len(p.Checked.Path()) > 0 || !c.checked[p.Checked.Fact()] {
		c.refuse(diag.PushUnchecked(at, string(p.Checked)))
	}
	if strings.TrimSpace(p.Message) == "" {
		c.refuse(diag.BadDeclaration(at, "a Push has no commit message"))
	}
	c.expect(at, p.Repo, "the repository of a Push", slugType)
	c.expect(at, p.Pull, "the pull request of a Push", pullType)
	c.expect(at, p.Branch, "the branch of a Push", slugType)
}

func (c *checker) title(at string, t model.Title) {
	format := t.Format.String()
	if strings.TrimSpace(format) == "" {
		c.refuse(diag.BadDeclaration(at, "a title is empty"))
	}
	if verbs := countVerbs(format); verbs != len(t.Values) {
		c.refuse(diag.BadDeclaration(at, fmt.Sprintf("a title's format has %d verbs and is given %d values", verbs, len(t.Values))))
	}
	for _, ref := range t.Values {
		if got, ok := c.typeOf(at, ref, "a title"); ok && !titleClean(got) {
			c.refuse(diag.TitleNotClean(at, string(ref), describe(got)))
		}
	}
}

func (c *checker) body(at string, b *model.Body) {
	if b == nil {
		return
	}
	if (b.Text == "") == (b.Showing == "") || b.Text != "" && b.Map != "" {
		c.refuse(diag.BadDeclaration(at, "a body is either literal text or a hostile fact shown through a map"))
		return
	}
	if b.Showing == "" {
		return
	}
	want, where := textType, "a body shown as it is"
	if b.Map != "" {
		entry, ok := c.entry(at, "map", b.Map, vocab.MapKind)
		if !ok {
			return
		}
		want, where = entry.Params[0], "what "+b.Map+" shows"
	}
	c.expect(at, b.Showing, where, want)
}

// unknown records that a step produces the fact name, of a type no one can know because the
// step names a part the vocabulary does not have. Reading it is then not a second problem.
func (c *checker) unknown(name string) {
	if _, produced := c.facts[name]; !produced {
		c.facts[name] = nil
	}
}

// produceNamed records that a step produces the fact name, which an observer or agent chose.
func (c *checker) produceNamed(at, name string, t reflect.Type) {
	if !workflowID.MatchString(name) || slices.Contains(reserved, name) {
		c.refuse(diag.BadDeclaration(at, fmt.Sprintf("%q cannot name a fact: a fact's name is a lowercase word, and %v are built in", name, reserved)))
		return
	}
	c.produce(at, name, t)
}

// produce records that a step produces the fact name, of the Go type t, and checks it against
// the model's declaration of it.
func (c *checker) produce(at, name string, t reflect.Type) {
	if _, twice := c.facts[name]; twice {
		c.refuse(diag.DuplicateFact(at, name))
		return
	}
	c.facts[name] = t
	declared, ok := c.declared[name]
	if !ok {
		c.refuse(diag.BadDeclaration(at, fmt.Sprintf("it produces the fact %q, and the workflow's facts do not declare it", name)))
		return
	}
	held, hostileFact := hostile.Held(t)
	if !hostileFact {
		held = t
	}
	if declared.Type != held.String() || declared.Hostile != hostileFact {
		c.refuse(diag.WrongType(at, name, "the fact it produces", describeFact(declared), describe(t)))
	}
}

// typeOf is the type ref names, if it names one that exists here. The fact "attempts" exists
// anywhere in the flow: the engine reads it the first time something needs it.
func (c *checker) typeOf(at string, ref model.Ref, where string) (reflect.Type, bool) {
	root := ref.Fact()
	t, ok := c.facts[root]
	if ok && t == nil {
		return nil, false
	}
	if !ok && root == model.AttemptsFact && !c.beforeRun {
		t, ok = triesType, true
		c.produce(at, model.AttemptsFact, triesType)
	}
	if !ok {
		c.refuse(diag.FactBeforeObserved(at, string(ref)))
		return nil, false
	}
	field, err := vocab.FieldType(t, ref.Path())
	var inside *vocab.InsideHostile
	if errors.As(err, &inside) {
		c.refuse(diag.HostileFact(at, string(ref), "a path into "+cmp.Or(inside.At, root)))
		return nil, false
	}
	if err != nil {
		c.refuse(diag.UnknownName(at, "field in "+where+" named", string(ref)))
		return nil, false
	}
	return field, true
}

// expect checks that ref names a value of type want.
func (c *checker) expect(at string, ref model.Ref, where string, want reflect.Type) {
	if got, ok := c.typeOf(at, ref, where); ok && got != want {
		_, wantHostile := hostile.Held(want)
		c.mismatch(at, ref, where, describe(want), wantHostile, got)
	}
}

// mismatch refuses ref, a got where a want goes: as hostile if a hostile value went where a
// clean one belongs, and as the wrong type otherwise.
func (c *checker) mismatch(at string, ref model.Ref, where, want string, wantHostile bool, got reflect.Type) {
	if _, hostileValue := hostile.Held(got); hostileValue && !wantHostile {
		c.refuse(diag.HostileFact(at, string(ref), where))
		return
	}
	c.refuse(diag.WrongType(at, string(ref), where, want, describe(got)))
}

// entry is the part name, which must be of kind.
func (c *checker) entry(at, what, name string, kind vocab.Kind) (*vocab.Entry, bool) {
	entry, ok := c.v.Lookup(name)
	if !ok || entry.Kind != kind {
		c.refuse(diag.UnknownName(at, what, name))
		return nil, false
	}
	return entry, true
}
