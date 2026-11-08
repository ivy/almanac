package check

import (
	"maps"
	"reflect"
	"slices"

	"github.com/ivy/almanac/hostile"
	"github.com/ivy/almanac/model"
)

// keyable reports whether a value of type t has a stable text form for a key.
func keyable(t reflect.Type) bool {
	return t == slugType || t == shaType || number(t) || t.Kind() == reflect.Bool
}

// titleClean reports whether a title may hold a value of type t, as hostile.Format.Title
// accepts one: a number, a bool, a slug or a sha, or a pointer to one.
func titleClean(t reflect.Type) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t == slugType || t == shaType || number(t) || t.Kind() == reflect.Bool
}

func number(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	default:
		return false
	}
}

// countVerbs counts the verbs in a title's format, which each take one value.
func countVerbs(format string) int {
	verbs := 0
	for i := 0; i < len(format); i++ {
		if format[i] != '%' {
			continue
		}
		if i+1 < len(format) && format[i+1] == '%' {
			i++
			continue
		}
		verbs++
	}
	return verbs
}

// describe names a type as a message shows it: "hostile string" for a hostile.Of[string].
func describe(t reflect.Type) string {
	if held, ok := hostile.Held(t); ok {
		return "hostile " + held.String()
	}
	return t.String()
}

func describeFact(f model.Fact) string {
	if f.Hostile {
		return "hostile " + f.Type
	}
	return f.Type
}

func sortedKeys[V any](m map[string]V) []string { return slices.Sorted(maps.Keys(m)) }
