package main

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// testSpec is a small spec with a field of every shape the generator reads.
const testSpec = `{
	"x-webhooks": {"thing-happened": {"post": {"requestBody": {"content": {"application/json": {"schema": {
		"$ref": "#/components/schemas/event"
	}}}}}}},
	"paths": {"/things/{id}": {"get": {"responses": {"200": {"content": {"application/json": {"schema": {
		"type": "object", "properties": {"title": {"type": "string"}}
	}}}}}}}},
	"components": {"schemas": {"event": {"type": "object", "properties": {
		"id": {"type": "integer"},
		"open": {"type": "boolean"},
		"title": {"type": "string"},
		"body": {"type": "string", "nullable": true},
		"branch": {"type": "string", "nullable": true},
		"state": {"type": "string", "enum": ["open", "timed_out", null], "nullable": true},
		"labels": {"type": "array", "items": {"type": "object", "properties": {"name": {"type": "string"}}}},
		"pulls": {"type": "array", "items": {"type": "object", "properties": {"number": {"type": "integer"}}}},
		"nested": {"allOf": [{"type": "object", "properties": {"deep": {"type": "string"}}}]},
		"meta": {"type": "object", "properties": {}}
	}}}}
}`

func testDocument() any {
	var document any
	Expect(json.Unmarshal([]byte(testSpec), &document)).To(Succeed())
	return document
}

func projection(fields string) string {
	return "[Thing]\nwebhook = \"thing-happened\"\nkind = \"thing\"\n" + fields + "\n"
}

func generated(fields string) string {
	out, err := generate(testDocument(), []byte(projection(fields)), "events")
	Expect(err).NotTo(HaveOccurred())
	return string(out)
}

func refusal(fields string) string {
	_, err := generate(testDocument(), []byte(projection(fields)), "events")
	Expect(err).To(HaveOccurred())
	return err.Error()
}

// fieldTypes is each field of the struct named typeName in source, with its type as written.
func fieldTypes(source, typeName string) map[string]string {
	files := token.NewFileSet()
	file, err := parser.ParseFile(files, "events.go", source, 0)
	Expect(err).NotTo(HaveOccurred())
	types := map[string]string{}
	ast.Inspect(file, func(node ast.Node) bool {
		spec, ok := node.(*ast.TypeSpec)
		if !ok || spec.Name.Name != typeName {
			return true
		}
		for _, f := range spec.Type.(*ast.StructType).Fields.List {
			var written bytes.Buffer
			Expect(printer.Fprint(&written, files, f.Type)).To(Succeed())
			for _, name := range f.Names {
				types[name.Name] = written.String()
			}
		}
		return false
	})
	return types
}

var _ = Describe("Generating events from a projection", func() {
	// xtask/tests/generate.rs: a_string_is_tainted_unless_a_validated_type_is_named
	It("keeps a string hostile unless a validated type is named", func() {
		out := generated("fields.title = { path = \"title\" }\nfields.id_text = { path = \"nested.deep\", as = \"Slug\" }")
		Expect(fieldTypes(out, "Thing")).To(Equal(map[string]string{
			"Title":  "hostile.Of[string]",
			"IDText": "hostile.Slug",
		}))
		Expect(out).To(ContainSubstring(`e.Title = p.Text("/title")`))
		Expect(out).To(ContainSubstring(`e.IDText = wire.Field[hostile.Slug](p, "/nested/deep")`))
	})

	// xtask/tests/generate.rs: a_typo_in_a_path_fails_with_the_path_named
	It("names the path and the missing part of a typo", func() {
		Expect(refusal("fields.t = { path = \"nested.dep\" }")).To(Equal("`nested.dep` is not in the spec (no `dep`)"))
	})

	// xtask/tests/generate.rs: a_validated_type_on_a_non_string_fails
	It("refuses a validated type on something other than a string", func() {
		Expect(refusal("fields.n = { path = \"id\", as = \"Slug\" }")).To(Equal("`id` is integer, cannot be `as Slug`"))
	})

	It("refuses a validated type it does not know", func() {
		Expect(refusal("fields.t = { path = \"title\", as = \"String\" }")).To(Equal("`String` is not a validated type: name Slug or Sha"))
	})

	// xtask/tests/generate.rs: generated_enums_keep_an_unknown_variant
	It("keeps an Unknown value in every enum it generates", func() {
		out := generated("fields.state = { path = \"state\" }")
		Expect(out).To(MatchRegexp(`ThingStateUnknown\s+= ThingState\{name: "unknown"\}`))
		Expect(out).To(MatchRegexp(`ThingStateTimedOut\s+= ThingState\{name: "timed_out"\}`))
		Expect(out).To(ContainSubstring("default:\n\t\t*v = ThingStateUnknown"))
		Expect(out).To(ContainSubstring("hostile.Closed"))
	})

	// xtask/tests/generate.rs: a_nullable_field_is_optional_or_stays_tainted
	It("makes a nullable field optional, or keeps it hostile", func() {
		out := generated("fields.branch = { path = \"branch\", as = \"Slug\" }\nfields.body = { path = \"body\" }\nfields.state = { path = \"state\" }")
		Expect(fieldTypes(out, "Thing")).To(Equal(map[string]string{
			"Branch": "*hostile.Slug",
			"Body":   "hostile.Of[*string]",
			"State":  "*ThingState",
		}))
		Expect(out).To(ContainSubstring(`wire.OptionalField[hostile.Slug](p, "/branch")`))
		Expect(out).To(ContainSubstring(`p.OptionalText("/body")`))
		Expect(out).To(ContainSubstring(`wire.OptionalMember[ThingState](p, "/state")`))
	})

	// xtask/tests/generate.rs: a_list_of_strings_stays_tainted_and_a_list_of_numbers_does_not
	It("keeps a list of strings hostile, and a list of numbers clean", func() {
		out := generated("fields.labels = { path = \"labels\", each = \"name\" }\nfields.pulls = { path = \"pulls\", each = \"number\" }")
		Expect(fieldTypes(out, "Thing")).To(Equal(map[string]string{
			"Labels": "hostile.Of[[]string]",
			"Pulls":  "[]uint64",
		}))
		Expect(out).To(ContainSubstring(`p.TextList("/labels", "/name")`))
		Expect(out).To(ContainSubstring(`wire.List[uint64](p, "/pulls", "/number")`))
	})

	// xtask/tests/generate.rs: a_typo_in_a_list_item_is_named
	It("names a typo in a list item", func() {
		Expect(refusal("fields.labels = { path = \"labels\", each = \"nme\" }")).To(Equal("`labels.nme` is not in the spec (no `nme`)"))
	})

	// xtask/tests/generate.rs: a_projection_can_map_a_rest_response_onto_the_event
	It("maps a REST response onto the event", func() {
		out := generated("rest = \"GET /things/{id}\"\nfields.title = { path = \"title\", rest = \"title\" }")
		Expect(out).To(ContainSubstring(`func (Thing) Operation() string { return "GET /things/{id}" }`))
		Expect(out).To(ContainSubstring(`{From: "/title", To: "/title"}`))
	})

	It("writes no REST mapping for an event that names no operation", func() {
		Expect(generated("fields.title = { path = \"title\" }")).NotTo(ContainSubstring("Projections"))
	})

	// xtask/tests/generate.rs: a_typo_in_a_rest_path_is_named
	It("names a typo in a REST path", func() {
		Expect(refusal("rest = \"GET /things/{id}\"\nfields.title = { path = \"title\", rest = \"titel\" }")).To(Equal("`titel` is not in the spec (no `titel`)"))
	})

	// xtask/tests/generate.rs: every_field_must_say_where_it_comes_from_in_a_rest_response
	It("needs every field to say where it is in a REST response", func() {
		Expect(refusal("rest = \"GET /things/{id}\"\nfields.title = { path = \"title\" }")).To(Equal("Thing.title has no `rest` path"))
	})

	// xtask/tests/generate.rs: an_operation_that_is_not_in_the_spec_is_named
	It("names an operation that is not in the spec", func() {
		Expect(refusal("rest = \"GET /nope\"\nfields.title = { path = \"title\", rest = \"title\" }")).To(Equal("`GET /nope` is not in the spec"))
	})

	// xtask/tests/generate.rs: a_webhook_that_is_not_in_the_spec_is_named
	It("names a webhook that is not in the spec", func() {
		_, err := generate(testDocument(), []byte("[T]\nwebhook = \"nope\"\nkind = \"t\"\n"), "events")
		Expect(err).To(MatchError("webhook `nope` is not in the spec"))
	})

	It("refuses a field an event cannot hold as it is", func() {
		Expect(refusal("fields.meta = { path = \"meta\" }")).To(Equal("`meta` is object, which an event cannot hold: name a field inside it, or use `each`"))
	})

	It("refuses a key it does not read, so a typo is not silently dropped", func() {
		Expect(refusal("fields.title = { path = \"title\", sa = \"Slug\" }")).To(ContainSubstring("Thing.fields.title.sa"))
	})

	It("keeps the projection's order, so its output is stable", func() {
		out := generated("fields.title = { path = \"title\" }\nfields.id = { path = \"id\" }\nfields.open = { path = \"open\" }")
		Expect(out).To(MatchRegexp(`(?s)Title .*ID .*Open .*`))
		Expect(generated("fields.title = { path = \"title\" }\nfields.id = { path = \"id\" }\nfields.open = { path = \"open\" }")).To(Equal(out))
	})
})

var _ = Describe("The eventgen command", func() {
	var dir, specPath, projectionPath, out string

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
		specPath = filepath.Join(dir, "spec.json")
		projectionPath = filepath.Join(dir, "projection.toml")
		out = filepath.Join(dir, "events.go")
		Expect(os.WriteFile(specPath, []byte(testSpec), 0o600)).To(Succeed())
		Expect(os.WriteFile(projectionPath, []byte(projection("fields.title = { path = \"title\" }")), 0o600)).To(Succeed())
	})

	args := func(more ...string) []string {
		return append([]string{"-spec", specPath, "-projection", projectionPath, "-out", out}, more...)
	}

	It("writes the events, and then finds them current", func() {
		Expect(run(args())).To(Succeed())
		Expect(run(args("-check"))).To(Succeed())
	})

	It("fails a check when the committed code is stale, and writes nothing", func() {
		Expect(run(args())).To(Succeed())
		Expect(os.WriteFile(projectionPath, []byte(projection("fields.id = { path = \"id\" }")), 0o600)).To(Succeed())
		before, err := os.ReadFile(out)
		Expect(err).NotTo(HaveOccurred())

		Expect(run(args("-check"))).To(MatchError(out + " is stale: run `make go-generate` and commit the result"))
		Expect(os.ReadFile(out)).To(Equal(before))
	})

	It("fails a check when nothing was generated yet", func() {
		Expect(run(args("-check"))).To(MatchError(ContainSubstring("is stale")))
	})

	It("names the projection a refusal comes from", func() {
		Expect(os.WriteFile(projectionPath, []byte(projection("fields.t = { path = \"nope\" }")), 0o600)).To(Succeed())
		Expect(run(args())).To(MatchError(projectionPath + ": `nope` is not in the spec (no `nope`)"))
	})

	It("keeps the committed events current with the pinned spec", func() {
		Expect(run([]string{
			"-spec", "../../specs/github/api.github.com.json",
			"-projection", "../../github/events/projection.toml",
			"-out", "../../github/events/events.go",
			"-check",
		})).To(Succeed())
	})
})
