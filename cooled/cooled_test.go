package cooled_test

import (
	"encoding/json"
	"reflect"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ivy/almanac/cooled"
)

const days3 = 3 * 24 * time.Hour

var now = time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)

func at(year int, month time.Month, day, hour, minute int) cooled.Published {
	return cooled.PublishedAt(time.Date(year, month, day, hour, minute, 0, 0, time.UTC))
}

func isCooled(published cooled.Published, minAge time.Duration) bool {
	proof, ok := cooled.Check(published, minAge, now)
	Expect(proof.IsZero()).To(Equal(!ok))
	return ok
}

// Ports crates/almanac/tests/cooled.rs.
var _ = Describe("Check", func() {
	It("a release older than the cooldown is cooled", func() {
		Expect(isCooled(at(2026, 11, 3, 11, 59), days3)).To(BeTrue())
	})

	It("a release exactly at the cooldown is cooled", func() {
		Expect(isCooled(at(2026, 11, 3, 12, 0), days3)).To(BeTrue())
	})

	It("a younger release is not cooled", func() {
		Expect(isCooled(at(2026, 11, 3, 12, 1), days3)).To(BeFalse())
	})

	It("a missing timestamp is never cooled", func() {
		Expect(isCooled(cooled.Published{}, 0)).To(BeFalse())
	})

	It("a timestamp in the future is never cooled", func() {
		Expect(isCooled(at(2030, 1, 1, 0, 0), 0)).To(BeFalse())
	})

	It("a release published this instant is not cooled, even with no cooldown", func() {
		Expect(isCooled(cooled.PublishedAt(now), 0)).To(BeFalse())
	})
})

var _ = Describe("Measure", func() {
	It("tells a release to wait for from one nothing can date", func() {
		age, proof := cooled.Measure(at(2026, 11, 3, 12, 1), days3, now)
		Expect(age).To(Equal(cooled.Young))
		Expect(proof.IsZero()).To(BeTrue())

		age, proof = cooled.Measure(cooled.Published{}, days3, now)
		Expect(age).To(Equal(cooled.Unknown))
		Expect(proof.IsZero()).To(BeTrue())
	})

	It("gives the proof only when the release has passed the cooldown", func() {
		age, proof := cooled.Measure(at(2026, 11, 1, 0, 0), days3, now)
		Expect(age).To(Equal(cooled.Passed))
		Expect(proof.IsZero()).To(BeFalse())
		Expect(age.String()).To(Equal("passed"))
	})
})

// recorded is a run's facts as a store would keep them.
type recorded struct{ Proof cooled.Cooled }

var _ = Describe("Cooled", func() {
	It("cannot be built outside its package, because it has no exported field", func() {
		t := reflect.TypeFor[cooled.Cooled]()
		for i := range t.NumField() {
			Expect(t.Field(i).IsExported()).To(BeFalse(), t.Field(i).Name)
		}
		Expect(cooled.Cooled{}.IsZero()).To(BeTrue())
	})

	It("does not survive being stored, so a recorded or forged one proves nothing", func() {
		proof, ok := cooled.Check(at(2026, 1, 1, 0, 0), days3, now)
		Expect(ok).To(BeTrue())

		stored, err := json.Marshal(recorded{Proof: proof})
		Expect(err).NotTo(HaveOccurred())
		var restored, forged recorded
		Expect(json.Unmarshal(stored, &restored)).To(Succeed())
		Expect(json.Unmarshal([]byte(`{"Proof": {"proven": true}}`), &forged)).To(Succeed())

		Expect(restored.Proof.IsZero()).To(BeTrue())
		Expect(forged.Proof.IsZero()).To(BeTrue())
	})
})

var _ = Describe("Published", func() {
	It("round-trips through JSON, as a run records it", func() {
		for _, published := range []cooled.Published{at(2026, 11, 3, 11, 59), {}} {
			stored, err := json.Marshal(published)
			Expect(err).NotTo(HaveOccurred())
			var restored cooled.Published
			Expect(json.Unmarshal(stored, &restored)).To(Succeed())
			Expect(restored).To(Equal(published))
		}
	})

	It("is unknown when stored as null", func() {
		var published cooled.Published
		Expect(json.Unmarshal([]byte(`null`), &published)).To(Succeed())
		_, known := published.At()
		Expect(known).To(BeFalse())
	})

	It("refuses anything but a time or null", func() {
		var published cooled.Published
		Expect(json.Unmarshal([]byte(`"last week"`), &published)).To(MatchError(ContainSubstring("RFC 3339")))
	})
})
