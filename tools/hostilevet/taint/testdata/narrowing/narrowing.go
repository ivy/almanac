// Package narrowing holds the functions hostile.Map, Narrow and Classify accept and refuse,
// and what a narrowing function may do with the text it reads.
package narrowing

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"github.com/ivy/almanac/hostile"
)

type Verdict struct {
	hostile.Closed
	name string
}

var (
	NoConcerns = Verdict{name: "no concerns"}
	Concerns   = Verdict{name: "concerns"}
)

// Narrowing functions that only answer.

func isFailed(conclusion string) bool {
	return conclusion == "failure" || strings.HasPrefix(conclusion, "timed_out")
}

func isLong(text string) bool { return len(text) > 3 }

func counted(text string) bool { return "n="+strconv.Itoa(len(text)) != "n=0" }

func indexed(text string) bool {
	at, rest, _ := strings.Cut(text, "|")
	index, err := strconv.Atoi(at)
	return err == nil && index < len(rest) && rest[index]&1 == 1
}

func verdictOf(text string) Verdict {
	lower := strings.ToLower(text)
	if strings.Contains(lower, "looks fine") {
		return NoConcerns
	}
	return Concerns
}

func same(pair hostile.Pair[string, string]) bool { return pair.First == pair.Second }

func anyLabel[T comparable](labels []T) bool { return len(labels) > 0 }

type Text string

func (t Text) Empty() bool { return t == "" }

type Matcher struct{ word string }

func (m Matcher) Matches(text string) bool { return strings.Contains(text, m.word) }

// A map keeps the text hostile, so its function may format it.

func quoted(text string) string { return "> " + text + fmt.Sprintf(" (%d)", len(text)) }

func accepted(title hostile.Of[string], labels hostile.Of[[]string]) {
	_ = hostile.Narrow(title, isFailed)
	_ = hostile.Narrow(title, isLong)
	_ = hostile.Narrow(title, counted)
	_ = hostile.Narrow(hostile.Join("3|", title), indexed)
	_ = hostile.Narrow(hostile.Zip(title, title), same)
	_ = hostile.Narrow(labels, anyLabel[string])
	_ = hostile.Narrow(labels, anyLabel)
	_ = hostile.Classify(title, verdictOf)
	_ = hostile.Map(title, quoted)
	_ = hostile.Map(title, strings.TrimSpace)
	_ = hostile.Narrow(hostile.New(Text("x")), Text.Empty)
}

// Functions that are not declared functions.

var check = isFailed

func refusedCallbacks(title hostile.Of[string], matcher Matcher) {
	var stolen string
	_ = hostile.Narrow(title, func(t string) bool { stolen = t; return true }) // want `hostile.Narrow takes a declared function, not a function literal`
	_ = hostile.Map(title, func(t string) int { return len(t) })               // want `hostile.Map takes a declared function, not a function literal`
	_ = hostile.Narrow(title, check)                                           // want `hostile.Narrow takes a declared function, not a function value`
	_ = hostile.Narrow(title, matcher.Matches)                                 // want `hostile.Narrow takes a declared function, not a bound method`
	_ = stolen
}

// Narrowing functions that carry the text.

var (
	stolen  string
	builder strings.Builder
	record  = map[string]string{}
	leaks   = make(chan string, 1)
)

type holder struct{ text string }

var shared = &holder{}

func formats(text string) bool {
	return fmt.Sprintf("%s", text) != "" // want `formats reads raw text for hostile.Narrow and formats it with fmt.Sprintf`
}

func concatenates(text string) bool {
	joined := "seen: " + text // want `concatenates reads raw text for hostile.Narrow and concatenates it with \+`
	return joined != ""
}

func appends(text string) bool {
	var all string
	all += text // want `appends reads raw text for hostile.Narrow and concatenates it with \+=`
	return all != ""
}

func converts(text string) bool {
	raw := []byte(text)      // want `converts reads raw text for hostile.Narrow and converts it with \[\]byte\(...\)`
	return string(raw) != "" // want `converts reads raw text for hostile.Narrow and converts it with string\(...\)`
}

func builds(text string) bool {
	var local bytes.Buffer
	local.WriteString(strings.TrimSpace(text)) // want `builds reads raw text for hostile.Narrow and writes it to a bytes.Buffer`
	builder.WriteString(text)                  // want `builds reads raw text for hostile.Narrow and writes it to a strings.Builder`
	return true
}

func stores(text string) bool {
	stolen = text             // want `stores reads raw text for hostile.Narrow and stores it outside the function`
	record["last"] = text[1:] // want `stores reads raw text for hostile.Narrow and stores it outside the function`
	return true
}

func storesThrough(text string) bool {
	local := holder{}
	local.text = text
	shared.text = local.text // want `storesThrough reads raw text for hostile.Narrow and stores it outside the function`
	return true
}

func sends(text string) bool {
	leaks <- text // want `sends reads raw text for hostile.Narrow and sends it on a channel`
	return true
}

func classifiesBy(text string) Verdict {
	for _, word := range strings.Fields(text) {
		stolen = word // want `classifiesBy reads raw text for hostile.Classify and stores it outside the function`
	}
	return Concerns
}

func refusedBodies(title hostile.Of[string]) {
	_ = hostile.Narrow(title, formats)
	_ = hostile.Narrow(title, concatenates)
	_ = hostile.Narrow(title, appends)
	_ = hostile.Narrow(title, converts)
	_ = hostile.Narrow(title, builds)
	_ = hostile.Narrow(title, stores)
	_ = hostile.Narrow(title, storesThrough)
	_ = hostile.Narrow(title, sends)
	_ = hostile.Classify(title, classifiesBy)
	_ = hostile.Narrow(hostile.Map(title, quoted), formats)
}
