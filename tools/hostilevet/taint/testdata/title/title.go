// Package title holds the values hostile.Titlef accepts and refuses.
package title

import "github.com/ivy/almanac/hostile"

type Severity struct {
	hostile.Closed
	name string
}

var High = Severity{name: "high"}

func (s Severity) String() string { return s.name }

func accepted(tool hostile.Slug, sha hostile.Sha, count uint64, ratio float64, failed bool, inner hostile.Title) {
	_ = hostile.Titlef("Fix 100%% of it")
	_ = hostile.Titlef("%s failed %d times", &tool, count)
	_ = hostile.Titlef("%s at %s: %.1f %t %s %v", inner, sha, ratio, failed, High, 3)
}

func refused(name string, title hostile.Of[string], names []string, err error, values []any) {
	_ = hostile.Titlef("%s failed", name)       // want `hostile.Titlef takes only clean values, and this is a string`
	_ = hostile.Titlef("%v", title)             // want `hostile.Titlef takes only clean values, and this is a github.com/ivy/almanac/hostile.Of\[string\]`
	_ = hostile.Titlef("%s and %v", "x", names) // want `this is a string` `this is a \[\]string`
	_ = hostile.Titlef("%v", err)               // want `this is a error`
	_ = hostile.Titlef("%v", values...)         // want `this is a \[\]any`
}
