package boundary

import (
	"encoding/json"

	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/hostile/reveal"
)

// A test may read what a boundary sends, through reveal or the stored form.
func sent(title hostile.Of[string]) (string, string) {
	encoded, _ := json.Marshal(title)
	var tagged map[string]string
	_ = json.Unmarshal(encoded, &tagged)
	return reveal.Value(title), tagged["$untrusted"]
}
