package main

import (
	"bytes"
	"errors"
	"fmt"
	"go/format"
	"go/token"
	"strings"

	"github.com/BurntSushi/toml"
)

// A projection declares the fields workflows use. A spec describes hundreds of fields and
// workflows need about ten, so events are projected, not generated whole. A string is hostile
// unless the projection names a validated type, because the spec types a title and a commit
// hash alike.

// declaredField is one field as the projection declares it.
type declaredField struct {
	// Path is where the field is in the webhook's payload, dotted.
	Path string `toml:"path"`
	// As names the validated type a string is read as: Slug or Sha.
	As string `toml:"as"`
	// Each is a field of every item of the list at Path, read as a list.
	Each string `toml:"each"`
	// Rest is where the field is in the REST response the event names, dotted.
	Rest string `toml:"rest"`
}

// declaredEvent is one event as the projection declares it.
type declaredEvent struct {
	Webhook string                   `toml:"webhook"`
	Kind    string                   `toml:"kind"`
	Rest    string                   `toml:"rest"`
	Fields  map[string]declaredField `toml:"fields"`
}

// validated are the types a string field may be read as, by the name the projection gives.
var validated = map[string]string{
	"Slug": "hostile.Slug",
	"Sha":  "hostile.Sha",
}

// field is one generated field.
type field struct {
	name string
	// json is the field's name in the projection, and in the event's stored form.
	json string
	typ  string
	// read is the expression that reads it from p, a *wire.Payload.
	read string
	// projection pairs the field's REST pointer with its webhook pointer, if it has one.
	projection *[2]string
}

// enum is a closed set generated for an enumerated field.
type enum struct {
	name   string
	about  string
	values []string
}

// event is one generated event.
type event struct {
	name, webhook, kind, rest string
	fields                    []field
	enums                     []enum
}

// generate returns Go source for package pkg holding every event projection declares, each
// field checked against spec.
func generate(document any, projection []byte, pkg string) ([]byte, error) {
	var declared map[string]declaredEvent
	meta, err := toml.Decode(string(projection), &declared)
	if err != nil {
		return nil, fmt.Errorf("the projection is not valid TOML: %w", err)
	}
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		return nil, fmt.Errorf("the projection has a key the generator does not read: %s", undecoded[0])
	}
	s := spec{root: document}
	var events []event
	for _, name := range topLevel(meta) {
		e, err := s.event(name, declared[name], fieldOrder(meta, name))
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return render(pkg, events)
}

// topLevel is the projection's events, in the order it declares them.
func topLevel(meta toml.MetaData) []string {
	var names []string
	for _, key := range meta.Keys() {
		if len(key) == 1 {
			names = append(names, key[0])
		}
	}
	return names
}

// fieldOrder is an event's fields, in the order the projection declares them.
func fieldOrder(meta toml.MetaData, name string) []string {
	var names []string
	for _, key := range meta.Keys() {
		if len(key) == 3 && key[0] == name && key[1] == "fields" {
			names = append(names, key[2])
		}
	}
	return names
}

func (s spec) event(name string, declared declaredEvent, order []string) (event, error) {
	if declared.Webhook == "" || declared.Kind == "" {
		return event{}, fmt.Errorf("%s needs `webhook` and `kind`", name)
	}
	root, err := s.webhook(declared.Webhook)
	if err != nil {
		return event{}, err
	}
	if len(order) == 0 {
		return event{}, fmt.Errorf("%s has no fields", name)
	}
	if !token.IsIdentifier(name) || !token.IsExported(name) {
		return event{}, fmt.Errorf("`%s` cannot name an exported Go type", name)
	}
	e := event{name: name, webhook: declared.Webhook, kind: declared.Kind, rest: declared.Rest}
	for _, fieldName := range order {
		f, generated, err := s.field(root, e, fieldName, declared.Fields[fieldName])
		if err != nil {
			return event{}, err
		}
		e.fields = append(e.fields, f)
		if generated != nil {
			e.enums = append(e.enums, *generated)
		}
	}
	if declared.Rest == "" {
		return e, nil
	}
	response, err := s.operation(declared.Rest)
	if err != nil {
		return event{}, err
	}
	for i, fieldName := range order {
		rest := declared.Fields[fieldName].Rest
		if rest == "" {
			return event{}, fmt.Errorf("%s.%s has no `rest` path", name, fieldName)
		}
		if _, err := s.at(response, rest); err != nil {
			return event{}, err
		}
		e.fields[i].projection = &[2]string{jsonPointer(rest), jsonPointer(declared.Fields[fieldName].Path)}
	}
	return e, nil
}

func (s spec) field(root any, e event, name string, declared declaredField) (field, *enum, error) {
	if declared.Path == "" {
		return field{}, nil, fmt.Errorf("%s.%s has no `path`", e.name, name)
	}
	path := declared.Path
	ptr := jsonPointer(path)
	f := field{name: camel(name), json: name}
	schemas, err := s.at(root, path)
	if err != nil {
		return field{}, nil, err
	}
	if declared.Each != "" {
		full := path + "." + declared.Each
		items, err := s.at(root, full)
		if err != nil {
			return field{}, nil, err
		}
		found, err := s.leaf(items, full)
		if err != nil {
			return field{}, nil, err
		}
		item := jsonPointer(declared.Each)
		switch found.kind {
		case "integer":
			f.typ, f.read = "[]uint64", fmt.Sprintf("wire.List[uint64](p, %q, %q)", ptr, item)
		case "string":
			f.typ, f.read = "hostile.Of[[]string]", fmt.Sprintf("p.TextList(%q, %q)", ptr, item)
		default:
			return field{}, nil, fmt.Errorf("`%s` is %s, which an event cannot hold", full, found.kind)
		}
		return f, nil, nil
	}
	found, err := s.leaf(schemas, path)
	if err != nil {
		return field{}, nil, err
	}
	clean := func(typ string) {
		if found.nullable {
			f.typ, f.read = "*"+typ, fmt.Sprintf("wire.OptionalField[%s](p, %q)", typ, ptr)
		} else {
			f.typ, f.read = typ, fmt.Sprintf("wire.Field[%s](p, %q)", typ, ptr)
		}
	}
	switch {
	case declared.As != "":
		typ, ok := validated[declared.As]
		if !ok {
			return field{}, nil, fmt.Errorf("`%s` is not a validated type: name Slug or Sha", declared.As)
		}
		if found.kind != "string" {
			return field{}, nil, fmt.Errorf("`%s` is %s, cannot be `as %s`", path, found.kind, declared.As)
		}
		clean(typ)
	case len(found.values) > 0:
		generated := enum{name: e.name + camel(name), about: path, values: found.values}
		if err := generated.check(); err != nil {
			return field{}, nil, err
		}
		if found.nullable {
			f.typ, f.read = "*"+generated.name, fmt.Sprintf("wire.OptionalMember[%s](p, %q)", generated.name, ptr)
		} else {
			f.typ, f.read = generated.name, fmt.Sprintf("wire.Member[%s](p, %q)", generated.name, ptr)
		}
		return f, &generated, nil
	case found.kind == "integer":
		clean("uint64")
	case found.kind == "boolean":
		clean("bool")
	case found.kind == "string" && found.nullable:
		f.typ, f.read = "hostile.Of[*string]", fmt.Sprintf("p.OptionalText(%q)", ptr)
	case found.kind == "string":
		f.typ, f.read = "hostile.Of[string]", fmt.Sprintf("p.Text(%q)", ptr)
	default:
		return field{}, nil, fmt.Errorf("`%s` is %s, which an event cannot hold: name a field inside it, or use `each`", path, found.kind)
	}
	return f, nil, nil
}

// unknown is the value every generated enum reads a value it does not know as (R-38).
const unknown = "Unknown"

// check refuses values that cannot name a Go constant, or that collide.
func (e enum) check() error {
	seen := map[string]string{unknown: unknown}
	for _, value := range e.values {
		name := camel(value)
		if !token.IsIdentifier(e.name + name) {
			return fmt.Errorf("`%s` value %q cannot name a Go constant", e.about, value)
		}
		if other, ok := seen[name]; ok {
			return fmt.Errorf("`%s` values %q and %q would both be %s%s", e.about, other, value, e.name, name)
		}
		seen[name] = value
	}
	return nil
}

// initialisms are written in capitals in Go names.
var initialisms = map[string]string{"id": "ID", "url": "URL", "api": "API", "http": "HTTP", "json": "JSON"}

// camel turns snake_case or kebab-case into an exported Go name.
func camel(text string) string {
	var out strings.Builder
	for part := range strings.FieldsFuncSeq(text, func(r rune) bool { return r == '_' || r == '-' }) {
		if upper, ok := initialisms[part]; ok {
			out.WriteString(upper)
			continue
		}
		out.WriteString(strings.ToUpper(part[:1]) + part[1:])
	}
	return out.String()
}

func jsonPointer(path string) string {
	return "/" + strings.ReplaceAll(path, ".", "/")
}

func render(pkg string, events []event) ([]byte, error) {
	var out bytes.Buffer
	w := func(format string, args ...any) { fmt.Fprintf(&out, format, args...) }
	hasEnums := false
	for _, e := range events {
		hasEnums = hasEnums || len(e.enums) > 0
	}
	w("// Code generated by tools/eventgen from projection.toml, checked against specs/github/api.github.com.json. DO NOT EDIT.\n\n")
	w("package %s\n\nimport (\n", pkg)
	if hasEnums {
		w("%q\n\n", "encoding/json")
	}
	w("%q\n%q\n)\n", "github.com/ivy/almanac/hostile", "github.com/ivy/almanac/wire")
	for _, e := range events {
		for _, en := range e.enums {
			renderEnum(w, e, en)
		}
		renderEvent(w, e)
	}
	formatted, err := format.Source(out.Bytes())
	if err != nil {
		return nil, errors.Join(errors.New("the generated code does not parse, which is a bug in tools/eventgen"), err)
	}
	return formatted, nil
}

func renderEnum(w func(string, ...any), e event, en enum) {
	w("\n// %s is %s in GitHub's %s webhook. A value GitHub adds later reads as %s%s.\n", en.name, en.about, e.webhook, en.name, unknown)
	w("type %s struct {\nhostile.Closed\nname string\n}\n\n", en.name)
	w("// The values of %s.\nvar (\n", en.name)
	for _, value := range en.values {
		w("%s%s = %s{name: %q}\n", en.name, camel(value), en.name, value)
	}
	w("%s%s = %s{name: %q}\n)\n\n", en.name, unknown, en.name, strings.ToLower(unknown))
	w("// String is the value as GitHub names it.\nfunc (v %s) String() string { return v.name }\n\n", en.name)
	w("// MarshalText writes the value as GitHub names it.\nfunc (v %s) MarshalText() ([]byte, error) { return []byte(v.name), nil }\n\n", en.name)
	w("// UnmarshalJSON reads a value GitHub names, and any other as %s%s.\n", en.name, unknown)
	w("func (v *%s) UnmarshalJSON(data []byte) error {\nvar name string\nif err := json.Unmarshal(data, &name); err != nil {\nreturn err\n}\nswitch name {\n", en.name)
	for _, value := range en.values {
		w("case %q:\n*v = %s%s\n", value, en.name, camel(value))
	}
	w("default:\n*v = %s%s\n}\nreturn nil\n}\n", en.name, unknown)
}

func renderEvent(w func(string, ...any), e event) {
	w("\n// %s is a %s delivery, projected from GitHub's %s webhook.\n", e.name, e.kind, e.webhook)
	w("type %s struct {\n", e.name)
	for _, f := range e.fields {
		w("%s %s `json:%q`\n", f.name, f.typ, f.json)
	}
	w("}\n\n")
	w("// Kind is the kind of delivery %s is parsed from.\nfunc (%s) Kind() string { return %q }\n\n", e.name, e.name, e.kind)
	w("// AlsoKinds is empty: %s is parsed from one kind only.\nfunc (%s) AlsoKinds() []string { return nil }\n\n", e.name, e.name)
	w("// Parse reads the projected fields from a payload.\nfunc (e *%s) Parse(p *wire.Payload) {\n", e.name)
	for _, f := range e.fields {
		w("e.%s = %s\n", f.name, f.read)
	}
	w("}\n")
	if e.rest == "" {
		return
	}
	w("\n// Operation is the REST operation a scan reads a %s from.\nfunc (%s) Operation() string { return %q }\n\n", e.name, e.name, e.rest)
	w("// Projections place the REST response's fields where the webhook has them.\nfunc (%s) Projections() []wire.Projection {\nreturn []wire.Projection{\n", e.name)
	for _, f := range e.fields {
		w("{From: %q, To: %q},\n", f.projection[0], f.projection[1])
	}
	w("}\n}\n")
}
