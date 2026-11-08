package vocab

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/ivy/almanac/hostile"
)

// FieldType is the type of the field path names inside a value of type t. Each name is a
// field as encoding/json names it: its tag's name, or else its Go name. A path cannot look
// inside a hostile value, whose fields only a narrowing function may read.
func FieldType(t reflect.Type, path []string) (reflect.Type, error) {
	for i, name := range path {
		if _, ok := hostile.Held(t); ok {
			return nil, &InsideHostile{At: strings.Join(path[:i], ".")}
		}
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		field, ok := jsonField(t, name)
		if !ok {
			return nil, fmt.Errorf("a %s has no field %q", t, name)
		}
		t = field.Type
	}
	return t, nil
}

// Field is the field path names inside value, as [FieldType] finds it. A nil pointer on the
// way gives the field's zero value.
func Field(value any, path []string) (any, error) {
	v := reflect.ValueOf(value)
	for i, name := range path {
		if !v.IsValid() {
			return nil, fmt.Errorf("no value to read %q from", strings.Join(path[:i+1], "."))
		}
		if _, ok := hostile.Held(v.Type()); ok {
			return nil, &InsideHostile{At: strings.Join(path[:i], ".")}
		}
		for v.Kind() == reflect.Pointer {
			if v.IsNil() {
				t, err := FieldType(v.Type(), path[i:])
				if err != nil {
					return nil, err
				}
				return reflect.Zero(t).Interface(), nil
			}
			v = v.Elem()
		}
		field, ok := jsonField(v.Type(), name)
		if !ok {
			return nil, fmt.Errorf("a %s has no field %q", v.Type(), name)
		}
		v = v.FieldByIndex(field.Index)
	}
	return v.Interface(), nil
}

// InsideHostile is a path that looks inside a hostile value.
type InsideHostile struct {
	// At is the path to the hostile value, empty if it is the value the path starts from.
	At string
}

func (e *InsideHostile) Error() string {
	if e.At == "" {
		return "the value is hostile, and no path may look inside it"
	}
	return e.At + " is hostile, and no path may look inside it"
}

func jsonField(t reflect.Type, name string) (reflect.StructField, bool) {
	if t.Kind() != reflect.Struct {
		return reflect.StructField{}, false
	}
	for _, field := range reflect.VisibleFields(t) {
		if !field.IsExported() || field.Anonymous {
			continue
		}
		tag, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if tag == "-" {
			continue
		}
		if tag == name || (tag == "" && field.Name == name) {
			return field, true
		}
	}
	return reflect.StructField{}, false
}
