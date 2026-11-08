// Package boundary is not a boundary, so it may neither reveal hostile text nor read its tag.
package boundary

import (
	"encoding/json"

	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/hostile/reveal" // want `only the boundary packages tools/hostilevet lists may import hostile/reveal`
)

const key = "$untrusted" // want `only package hostile reads the \$untrusted tag`

type stored struct {
	Text string `json:"$untrusted"` // want `only package hostile decodes the \$untrusted tag`
}

type labelled struct {
	Text string `json:"text,omitempty" db:"$untrusted"`
}

func read(title hostile.Of[string]) string {
	encoded, _ := json.Marshal(title)
	var tagged map[string]string
	_ = json.Unmarshal(encoded, &tagged)
	_ = tagged["$untr"+"usted"] // want `only package hostile reads the \$untrusted tag`
	_ = tagged[key]
	_ = labelled{}
	_ = stored{}
	return reveal.Value(title)
}
