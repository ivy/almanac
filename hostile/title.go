package hostile

import (
	"fmt"
	"reflect"
)

// Title is text a tracker shows as it is: an issue's title, or a question. Only [Titlef]
// makes one, so it holds nothing but a literal and clean values.
type Title struct{ text string }

func (t Title) String() string { return t.text }

// literal is unexported, so only an untyped string constant converts to it: a string
// variable, which could hold hostile text, does not compile as a format.
type literal string

// Titlef builds a title from a literal format and clean values, formatted as fmt.Sprintf
// does: Titlef("%s failed %d times", tool, count).
//
// Each value must be a number, a bool, a [Slug], a [Sha], a [Title], a value of a [Closed]
// type, or a pointer to one. tools/hostilevet refuses anything else where it is written, and
// Titlef panics if it is given anything else anyway.
func Titlef(format literal, values ...any) Title {
	for i, value := range values {
		if !clean(value) {
			panic(fmt.Sprintf("hostile.Titlef: value %d is a %T, and a title holds only literals and clean values", i+1, value))
		}
	}
	return Title{text: fmt.Sprintf(string(format), values...)}
}

// clean reports whether value is a type a title may hold.
func clean(value any) bool {
	switch value.(type) {
	case Slug, Sha, Title, interface{ closed() }:
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	case reflect.Pointer:
		return !v.IsNil() && clean(v.Elem().Interface())
	default:
		return false
	}
}
