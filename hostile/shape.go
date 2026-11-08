package hostile

import (
	"encoding"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"time"
)

// CheckClean reports why values of type t could carry free text once stored as JSON, or nil
// if they cannot. A clean type holds only numbers, bools, a [Slug], a [Sha], a time, a type
// that validates itself when decoded (such as a [Closed] set), and structs, slices, maps and
// pointers of those. A string, a byte slice, an interface or a hostile value is not clean.
//
// A type that implements json.Unmarshaler or encoding.TextUnmarshaler counts as clean because
// it decides what it accepts. That decoding code is trusted, as a narrowing function is.
func CheckClean(t reflect.Type) error {
	return checkShape(t, false)
}

// CheckObserved reports why values of type t could carry free text that is not hostile, or
// nil if they cannot: what is read from outside must be clean or an [Of]. It is
// [CheckClean], with hostile values allowed anywhere.
func CheckObserved(t reflect.Type) error {
	return checkShape(t, true)
}

// ShapeError is a type that could carry free text. It names the type and where in it the text
// could be, never a value.
type ShapeError struct {
	// Type is the type that was checked.
	Type reflect.Type
	// Path is where in it the free text could be, such as ".Title" or ".Lines[]"; empty
	// for the type itself.
	Path string
	// Holds is the kind of type found there, such as "string".
	Holds string
	// Observed is whether a hostile value would have been allowed there.
	Observed bool
}

func (e *ShapeError) Error() string {
	next := "make it a number, a bool, a validated type such as hostile.Slug, or a closed set"
	if e.Observed {
		next = "make it a hostile.Of, a number, a bool, a validated type such as hostile.Slug, or a closed set"
	}
	if e.Type == nil {
		return "no type to check: pass a value of the type to record"
	}
	at := "it is"
	if e.Path != "" {
		at = e.Path + " is"
	}
	return fmt.Sprintf("%s could carry free text, because %s %s: %s", e.Type, at, e.Holds, next)
}

var (
	opens           = reflect.TypeFor[opener]()
	rawMessage      = reflect.TypeFor[json.RawMessage]()
	jsonUnmarshaler = reflect.TypeFor[json.Unmarshaler]()
	textUnmarshaler = reflect.TypeFor[encoding.TextUnmarshaler]()
	title           = reflect.TypeFor[Title]()
	closedSet       = reflect.TypeFor[interface{ closed() }]()
	alwaysClean     = []reflect.Type{reflect.TypeFor[Slug](), reflect.TypeFor[Sha](), reflect.TypeFor[time.Time]()}
)

// decodesItself reports whether t validates what it accepts when it is decoded.
func decodesItself(t reflect.Type) bool {
	pointer := reflect.PointerTo(t)
	return pointer.Implements(jsonUnmarshaler) || pointer.Implements(textUnmarshaler)
}

func checkShape(t reflect.Type, observed bool) error {
	shape := shapeCheck{root: t, observed: observed, seen: map[reflect.Type]bool{}}
	if t == nil {
		return &ShapeError{Observed: observed, Holds: "nothing"}
	}
	return shape.walk(t, "")
}

type shapeCheck struct {
	root     reflect.Type
	observed bool
	seen     map[reflect.Type]bool
}

func (s shapeCheck) refuse(path, holds string) error {
	return &ShapeError{Type: s.root, Path: path, Holds: holds, Observed: s.observed}
}

func (s shapeCheck) walk(t reflect.Type, path string) error {
	if s.seen[t] {
		return nil
	}
	s.seen[t] = true
	switch {
	case t.Implements(opens):
		if s.observed {
			return nil
		}
		return s.refuse(path, "a hostile value")
	case t == rawMessage:
		return s.refuse(path, "raw JSON")
	case t == title:
		// A Title has no JSON form, because decoding one would make a title from data.
		return s.refuse(path, "a title, which has no stored form")
	case slices.Contains(alwaysClean, t), decodesItself(t):
		return nil
	case t.Kind() != reflect.Pointer && t.Implements(closedSet):
		// A pointer to a closed set is checked as the set itself, below. Decoded field by field, it would come out as a value outside its declared set.
		return s.refuse(path, "a closed set with no UnmarshalJSON to choose among its values")
	}
	switch t.Kind() {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return nil
	case reflect.Pointer, reflect.Array:
		return s.walk(t.Elem(), path)
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return s.refuse(path, "a byte slice")
		}
		return s.walk(t.Elem(), path+"[]")
	case reflect.Map:
		if err := s.walk(t.Key(), path+"{key}"); err != nil {
			return err
		}
		return s.walk(t.Elem(), path+"{}")
	case reflect.Struct:
		for field := range fields(t) {
			if err := s.walk(field.Type, path+"."+field.Name); err != nil {
				return err
			}
		}
		return nil
	case reflect.String:
		return s.refuse(path, "a string")
	case reflect.Interface:
		return s.refuse(path, "an interface, which can hold anything")
	default:
		return s.refuse(path, "a "+t.Kind().String()+", which JSON cannot hold")
	}
}

// fields are the fields of a struct that encoding/json reads and writes.
func fields(t reflect.Type) func(yield func(reflect.StructField) bool) {
	return func(yield func(reflect.StructField) bool) {
		for i := range t.NumField() {
			field := t.Field(i)
			if (!field.IsExported() && !field.Anonymous) || field.Tag.Get("json") == "-" {
				continue
			}
			if !yield(field) {
				return
			}
		}
	}
}

// Decode reads hostile JSON as a value of type T. The value stays hostile. An error names T
// and never quotes the JSON.
func Decode[T any](h Of[json.RawMessage]) (Of[T], error) {
	var value T
	if err := json.Unmarshal(h.value(), &value); err != nil {
		return Of[T]{}, fmt.Errorf("hostile value: does not decode as a %T", value)
	}
	return New(value), nil
}

// DecodeClean reads hostile JSON into out, which must point to a clean type ([CheckClean]).
// Decoding into such a type is how text leaves taint as data: what comes out is numbers, bools,
// validated text and closed values. An error names the type and never quotes the JSON.
func DecodeClean(h Of[json.RawMessage], out any) error {
	target := reflect.ValueOf(out)
	if target.Kind() != reflect.Pointer || target.IsNil() {
		return fmt.Errorf("hostile.DecodeClean: out is a %T, and must be a non-nil pointer", out)
	}
	if err := CheckClean(target.Type().Elem()); err != nil {
		return err
	}
	fresh := reflect.New(target.Type().Elem())
	if err := json.Unmarshal(h.value(), fresh.Interface()); err != nil {
		return fmt.Errorf("hostile value: does not decode as a %s", target.Type().Elem())
	}
	target.Elem().Set(fresh.Elem())
	return nil
}

// Held reports whether t is an [Of], and if so the type it holds. A checker uses it to tell a
// hostile value's type from a clean one without a value in hand.
func Held(t reflect.Type) (reflect.Type, bool) {
	if t == nil || t.Kind() != reflect.Struct || !t.Implements(holds) {
		return nil, false
	}
	return reflect.Zero(t).Interface().(holder).held(), true
}

// holder is implemented only by Of, for Held.
type holder interface{ held() reflect.Type }

var holds = reflect.TypeFor[holder]()

func (Of[T]) held() reflect.Type { return reflect.TypeFor[T]() }
