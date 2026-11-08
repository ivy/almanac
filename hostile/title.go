package hostile

import (
	"fmt"
	"reflect"
)

// Title is text a tracker shows as it is: an issue's title, or a question. Only [Titlef] and
// [Format.Title] make one, so it holds nothing but a literal and clean values.
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
	return NewFormat(format).Title(values...)
}

// Format is a title's format held as a value, so that a workflow written as data can carry
// it until a run has the values to fill it. Like the format [Titlef] takes, it is made only
// from a string constant.
type Format struct{ text string }

// NewFormat holds a literal format for a title to be made later.
func NewFormat(format literal) Format { return Format{text: string(format)} }

// String is the format as it was written.
func (f Format) String() string { return f.text }

// MarshalText writes the format as it was written. There is no UnmarshalText: a format read
// from data would be a format that is not a literal.
func (f Format) MarshalText() ([]byte, error) { return []byte(f.text), nil }

// Title fills the format with values, which must be clean as [Titlef]'s are. It panics if one
// is not.
func (f Format) Title(values ...any) Title {
	for i, value := range values {
		if !clean(value) {
			panic(fmt.Sprintf("hostile: title value %d is a %T, and a title holds only literals and clean values", i+1, value))
		}
	}
	return Title{text: fmt.Sprintf(f.text, values...)}
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
