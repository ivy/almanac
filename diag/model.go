package diag

import (
	"errors"
	"fmt"
	"strings"
)

// Codes for a workflow model the checker refuses, and for a vocabulary that cannot be built.
// Each names the rule broken; the message names the workflow, step and rule where it is broken.
const (
	ModelUnknownName        Code = "almanac::model::unknown_name"
	ModelFactBeforeObserved Code = "almanac::model::fact_before_observed"
	ModelEffectNotAllowed   Code = "almanac::model::effect_not_allowed"
	ModelHostileFact        Code = "almanac::model::hostile_fact"
	ModelWrongType          Code = "almanac::model::wrong_type"
	ModelTitleNotClean      Code = "almanac::model::title_not_clean"
	ModelAgentInputHostile  Code = "almanac::model::agent_input_hostile"
	ModelPromptVars         Code = "almanac::model::prompt_vars"
	ModelPushUnchecked      Code = "almanac::model::push_unchecked"
	ModelFixerBeforeMeasure Code = "almanac::model::fixer_before_measure"
	ModelNoEnding           Code = "almanac::model::no_ending"
	ModelUnreachable        Code = "almanac::model::unreachable"
	ModelDuplicateID        Code = "almanac::model::duplicate_id"
	ModelDuplicateFact      Code = "almanac::model::duplicate_fact"
	ModelUnlabelled         Code = "almanac::model::unlabelled"
	ModelDeclaration        Code = "almanac::model::declaration"
	VocabDuplicate          Code = "almanac::vocab::duplicate"
	VocabInvalid            Code = "almanac::vocab::invalid"
)

func modelCodes() []Code {
	return []Code{
		ModelUnknownName, ModelFactBeforeObserved, ModelEffectNotAllowed, ModelHostileFact,
		ModelWrongType, ModelTitleNotClean, ModelAgentInputHostile, ModelPromptVars,
		ModelPushUnchecked, ModelFixerBeforeMeasure, ModelNoEnding, ModelUnreachable,
		ModelDuplicateID, ModelDuplicateFact, ModelUnlabelled, ModelDeclaration,
		VocabDuplicate, VocabInvalid,
	}
}

// In every constructor below, at says where in a workflow the problem is, such as
// `build_failures, step "fixer"` or `build_failures, step "decide", rule "push"`.

// UnknownName is a model naming kind of vocabulary, such as a predicate, that the vocabulary
// does not hold.
func UnknownName(at, kind, name string) *Error {
	return &Error{
		Code:    ModelUnknownName,
		Message: fmt.Sprintf("%s: the vocabulary has no %s named `%s`", at, kind, name),
		Help:    "fix the name, or register the part in the almanac's vocabulary; a model can only combine what the vocabulary holds",
	}
}

// FactBeforeObserved is a reference to a fact that no earlier step produces.
func FactBeforeObserved(at, ref string) *Error {
	return &Error{
		Code:    ModelFactBeforeObserved,
		Message: fmt.Sprintf("%s reads `%s`, and no earlier step produces it", at, ref),
		Help:    "produce the fact in an earlier step, or read it later; when, key and subjects read only the event",
	}
}

// EffectNotAllowed is an effect, or a hold, of a kind the workflow does not declare it may
// produce.
func EffectNotAllowed(at, what string) *Error {
	return &Error{
		Code:    ModelEffectNotAllowed,
		Message: fmt.Sprintf("%s %s, and the workflow does not declare that it may", at, what),
		Help:    "add the kind to may, where review sees it, or remove the effect",
	}
}

// HostileFact is a hostile value where only a clean one may go.
func HostileFact(at, ref, where string) *Error {
	return &Error{
		Code:    ModelHostileFact,
		Message: fmt.Sprintf("%s uses `%s` as %s, and it is hostile", at, ref, where),
		Help:    "test it with a narrowing predicate, show it through a map, or hand it to an agent as input; nothing else may read it",
	}
}

// WrongType is a reference whose type is not the one its place takes.
func WrongType(at, ref, where, want, got string) *Error {
	return &Error{
		Code:    ModelWrongType,
		Message: fmt.Sprintf("%s uses `%s` as %s, which takes a %s, and it is a %s", at, ref, where, want, got),
		Help:    "reference a fact or field of the type the vocabulary declares, or use a part that takes this type",
	}
}

// TitleNotClean is a title filled from something other than a clean number, bool, slug or sha.
func TitleNotClean(at, ref, got string) *Error {
	return &Error{
		Code:    ModelTitleNotClean,
		Message: fmt.Sprintf("%s fills a title with `%s`, a %s, and a title holds only literals, numbers, bools, slugs and shas", at, ref, got),
		Help:    "put the text in the body, through a map, or fill the title from a clean field",
	}
}

// AgentInputHostile is hostile input to an agent whose profile can change things.
func AgentInputHostile(at, agentName, input string) *Error {
	return &Error{
		Code:    ModelAgentInputHostile,
		Message: fmt.Sprintf("%s gives the worker agent `%s` the input `%s`, which is or holds hostile text", at, agentName, input),
		Help:    "give a worker only clean input, or read hostile text with a quarantined agent and act on its classified answer",
	}
}

// PromptVars is an agent step whose prompt variables do not match the prompt, or are not clean.
func PromptVars(at, agentName, problem string) *Error {
	return &Error{
		Code:    ModelPromptVars,
		Message: fmt.Sprintf("%s fills the prompt of `%s` wrongly: %s", at, agentName, problem),
		Help:    "fill exactly the variables the prompt declares, each from a clean fact",
	}
}

// PushUnchecked is a push of something other than a patch a check_patch step accepted.
func PushUnchecked(at, ref string) *Error {
	return &Error{
		Code:    ModelPushUnchecked,
		Message: fmt.Sprintf("%s pushes `%s`, which no check_patch step produced", at, ref),
		Help:    "check the agent's patch with a check_patch step, and push the fact it produces",
	}
}

// FixerBeforeMeasure is a fixer agent called before anything measured the update's age.
func FixerBeforeMeasure(at, agentName string) *Error {
	return &Error{
		Code:    ModelFixerBeforeMeasure,
		Message: fmt.Sprintf("%s calls the fixer `%s`, and no earlier step measures the release's age", at, agentName),
		Help:    "add a measure step before the agent, and end the run in a decide when the age is young or unknown",
	}
}

// NoEnding is a flow that can run off its end, or a decide with nothing to decide.
func NoEnding(at, problem string) *Error {
	return &Error{
		Code:    ModelNoEnding,
		Message: fmt.Sprintf("%s: %s", at, problem),
		Help:    "end the flow with a decide whose last rule is an otherwise rule, so every run ends where a person can see it",
	}
}

// UnreachableStep is a step or rule that no run can reach.
func UnreachableStep(at, problem string) *Error {
	return &Error{
		Code:    ModelUnreachable,
		Message: fmt.Sprintf("%s: %s", at, problem),
		Help:    "remove it, or move the otherwise rule before it to the end",
	}
}

// DuplicateID is a step or rule ID used twice in one workflow.
func DuplicateID(at, id string) *Error {
	return &Error{
		Code:    ModelDuplicateID,
		Message: fmt.Sprintf("%s uses the ID `%s`, which an earlier step or rule already has", at, id),
		Help:    "give every step and rule its own ID: runs, views and diffs name them by it",
	}
}

// DuplicateFact is a fact produced twice, or declared twice with different types.
func DuplicateFact(at, fact string) *Error {
	return &Error{
		Code:    ModelDuplicateFact,
		Message: fmt.Sprintf("%s produces the fact `%s`, which already exists", at, fact),
		Help:    "give the fact its own name; a fact is produced once per run",
	}
}

// Unlabelled is a step or rule with no ID or no label.
func Unlabelled(at, missing string) *Error {
	return &Error{
		Code:    ModelUnlabelled,
		Message: fmt.Sprintf("%s has no %s", at, missing),
		Help:    "give every step and rule an ID and a label a person can read; views show the label",
	}
}

// BadDeclaration is a workflow declaration that is missing or out of range: its name, intent,
// trigger, key, limits, may, or facts.
func BadDeclaration(at, problem string) *Error {
	return &Error{
		Code:    ModelDeclaration,
		Message: fmt.Sprintf("%s: %s", at, problem),
		Help:    "fix the declaration the message names; every workflow declares a name, an intent, a trigger, a key and a positive runs-per-hour",
	}
}

// DuplicateVocabulary is a vocabulary part registered under a name another part has.
func DuplicateVocabulary(name string) *Error {
	return &Error{
		Code:    VocabDuplicate,
		Message: fmt.Sprintf("two parts of the vocabulary are named `%s`", name),
		Help:    "rename one: a model refers to a part only by its name, so names must be unique",
	}
}

// InvalidVocabulary is a vocabulary part that cannot be registered.
func InvalidVocabulary(name, problem string) *Error {
	return &Error{
		Code:    VocabInvalid,
		Message: fmt.Sprintf("the vocabulary part `%s` cannot be registered: %s", name, problem),
		Help:    "fix the registration where the almanac builds its vocabulary",
	}
}

// Problems are several errors reported together, such as everything a checker refused in one
// workflow. Each is usually a *Error.
type Problems []error

func (p Problems) Error() string {
	texts := make([]string, 0, len(p))
	for _, problem := range p {
		texts = append(texts, problem.Error())
	}
	return strings.Join(texts, "\n")
}

// Unwrap lets errors.Is and errors.As reach each problem.
func (p Problems) Unwrap() []error { return p }

// Codes are the codes of the problems that have one, in order.
func (p Problems) Codes() []Code {
	codes := make([]Code, 0, len(p))
	for _, problem := range p {
		var coded *Error
		if errors.As(problem, &coded) {
			codes = append(codes, coded.Code)
		}
	}
	return codes
}
