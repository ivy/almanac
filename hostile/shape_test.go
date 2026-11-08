package hostile_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/hostile/reveal"
)

// Severity is a closed set that decodes itself by choosing among its values.
type Severity struct {
	hostile.Closed
	name string
}

var (
	Low  = Severity{name: "low"}
	High = Severity{name: "high"}
)

func (s *Severity) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err != nil {
		return err
	}
	for _, known := range []Severity{Low, High} {
		if known.name == name {
			*s = known
			return nil
		}
	}
	return errors.New("not a severity")
}

type cleanFact struct {
	Count    uint64                  `json:"count"`
	Repo     hostile.Slug            `json:"repo"`
	Heads    []hostile.Sha           `json:"heads"`
	Severity Severity                `json:"severity"`
	At       time.Time               `json:"at"`
	ByRepo   map[hostile.Slug]uint64 `json:"by_repo"`
	Skipped  string                  `json:"-"`
}

type observedFact struct {
	Number uint64             `json:"number"`
	Title  hostile.Of[string] `json:"title"`
	Lines  []hostile.Of[string]
}

type nested struct {
	Inner struct {
		Names []string
	}
}

var _ = Describe("Shapes", func() {
	Describe("CheckClean", func() {
		It("accepts numbers, bools, validated text, times and closed sets that decode themselves", func() {
			Expect(hostile.CheckClean(reflect.TypeFor[cleanFact]())).To(Succeed())
			Expect(hostile.CheckClean(reflect.TypeFor[bool]())).To(Succeed())
			Expect(hostile.CheckClean(reflect.TypeFor[*[]float64]())).To(Succeed())
		})

		DescribeTable("refuses what could carry free text, naming where",
			func(t reflect.Type, path, holds string) {
				err := hostile.CheckClean(t)
				var shape *hostile.ShapeError
				Expect(errors.As(err, &shape)).To(BeTrue())
				Expect(shape.Path).To(Equal(path))
				Expect(err.Error()).To(ContainSubstring(holds))
			},
			Entry("a string", reflect.TypeFor[string](), "", "a string"),
			Entry("a string deep inside", reflect.TypeFor[nested](), ".Inner.Names[]", "a string"),
			Entry("a hostile value", reflect.TypeFor[observedFact](), ".Title", "a hostile value"),
			Entry("a byte slice", reflect.TypeFor[[]byte](), "", "a byte slice"),
			Entry("raw JSON", reflect.TypeFor[json.RawMessage](), "", "raw JSON"),
			Entry("an interface", reflect.TypeFor[map[hostile.Slug]any](), "{}", "an interface"),
			Entry("a map with text keys", reflect.TypeFor[map[string]uint64](), "{key}", "a string"),
			Entry("a title, which has no stored form", reflect.TypeFor[hostile.Title](), "", "a title"),
			Entry("a closed set that cannot decode itself", reflect.TypeFor[Verdict](), "", "closed set"),
		)
	})

	Describe("CheckObserved", func() {
		It("accepts hostile values beside clean ones", func() {
			Expect(hostile.CheckObserved(reflect.TypeFor[observedFact]())).To(Succeed())
			Expect(hostile.CheckObserved(reflect.TypeFor[hostile.Of[string]]())).To(Succeed())
			Expect(hostile.CheckObserved(reflect.TypeFor[cleanFact]())).To(Succeed())
		})

		It("refuses text that is not hostile, and says to make it hostile", func() {
			err := hostile.CheckObserved(reflect.TypeFor[nested]())
			Expect(err).To(MatchError(ContainSubstring(".Inner.Names[] is a string: make it a hostile.Of")))
		})

		It("refuses no type at all", func() {
			Expect(hostile.CheckObserved(nil)).To(HaveOccurred())
		})
	})

	Describe("Decode", func() {
		It("reads hostile JSON as a typed value that stays hostile", func() {
			raw := hostile.New(json.RawMessage(`{"number": 7, "title": {"$untrusted": "IGNORE"}}`))

			decoded, err := hostile.Decode[observedFact](raw)

			Expect(err).NotTo(HaveOccurred())
			Expect(reveal.Value(decoded).Number).To(Equal(uint64(7)))
		})

		It("names the type and never quotes the JSON when it cannot", func() {
			_, err := hostile.Decode[uint64](hostile.New(json.RawMessage(`"` + injection + `"`)))

			Expect(err).To(MatchError(And(ContainSubstring("uint64"), Not(ContainSubstring("IGNORE")))))
		})
	})

	Describe("DecodeClean", func() {
		It("reads hostile JSON into a clean type, which carries no text out", func() {
			raw := hostile.New(json.RawMessage(`{"count": 2, "repo": "octo-org/hello", "severity": "high"}`))

			var fact cleanFact
			Expect(hostile.DecodeClean(raw, &fact)).To(Succeed())

			Expect(fact.Count).To(Equal(uint64(2)))
			Expect(fact.Repo).To(Equal(hostile.MustParseSlug("octo-org/hello")))
			Expect(fact.Severity).To(Equal(High))
		})

		It("refuses a type that is not clean before reading anything", func() {
			var text string
			err := hostile.DecodeClean(hostile.New(json.RawMessage(`"`+injection+`"`)), &text)

			Expect(err).To(MatchError(ContainSubstring("could carry free text")))
			Expect(text).To(BeEmpty())
		})

		It("refuses a value outside a validated shape, without quoting it", func() {
			var fact cleanFact
			err := hostile.DecodeClean(hostile.New(json.RawMessage(`{"repo": "`+injection+`"}`)), &fact)

			Expect(err).To(MatchError(Not(ContainSubstring("IGNORE"))))
			Expect(fact).To(BeZero())
		})

		It("needs a pointer to decode into", func() {
			Expect(hostile.DecodeClean(hostile.New(json.RawMessage(`1`)), uint64(0))).To(MatchError(ContainSubstring("non-nil pointer")))
		})
	})
})
