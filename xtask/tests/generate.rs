use serde_json::{Value, json};
use xtask::generate;

fn spec() -> Value {
    json!({
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
        }}}},
    })
}

fn projection(fields: &str) -> String {
    format!("[Thing]\nwebhook = \"thing-happened\"\nkind = \"thing\"\n{fields}\n")
}

fn generated(fields: &str) -> String {
    generate(&spec(), &projection(fields)).unwrap()
}

fn error(fields: &str) -> String {
    generate(&spec(), &projection(fields)).unwrap_err().0
}

#[test]
fn a_string_is_tainted_unless_a_validated_type_is_named() {
    let out = generated(
        "fields.title = { path = \"title\" }\nfields.id_text = { path = \"nested.deep\", as = \"Slug\" }",
    );
    assert!(out.contains("pub title: Untrusted<String>,"), "{out}");
    assert!(out.contains("pub id_text: Slug,"), "{out}");
}

#[test]
fn a_typo_in_a_path_fails_with_the_path_named() {
    let message = error("fields.t = { path = \"nested.dep\" }");
    assert_eq!(message, "`nested.dep` is not in the spec (no `dep`)");
}

#[test]
fn a_validated_type_on_a_non_string_fails() {
    let message = error("fields.n = { path = \"id\", as = \"Slug\" }");
    assert_eq!(message, "`id` is integer, cannot be `as Slug`");
}

#[test]
fn generated_enums_keep_an_unknown_variant() {
    let out = generated("fields.state = { path = \"state\" }");
    assert!(out.contains("#[serde(other)] Unknown,"), "{out}");
    assert!(
        out.contains("#[serde(rename = \"timed_out\")] TimedOut,"),
        "{out}"
    );
}

#[test]
fn a_nullable_field_is_optional_or_stays_tainted() {
    let out = generated(
        "fields.branch = { path = \"branch\", as = \"Slug\" }\nfields.body = { path = \"body\" }\nfields.state = { path = \"state\" }",
    );
    assert!(out.contains("pub branch: Option<Slug>,"), "{out}");
    assert!(
        out.contains("pub body: Untrusted<Option<String>>,"),
        "{out}"
    );
    assert!(out.contains("pub state: Option<ThingState>,"), "{out}");
}

#[test]
fn a_list_of_strings_stays_tainted_and_a_list_of_numbers_does_not() {
    let out = generated(
        "fields.labels = { path = \"labels\", each = \"name\" }\nfields.pulls = { path = \"pulls\", each = \"number\" }",
    );
    assert!(out.contains("pub labels: Untrusted<Vec<String>>,"), "{out}");
    assert!(
        out.contains("p.text_list(\"/labels\", \"/name\")?"),
        "{out}"
    );
    assert!(out.contains("pub pulls: Vec<u64>,"), "{out}");
}

#[test]
fn a_typo_in_a_list_item_is_named() {
    let message = error("fields.labels = { path = \"labels\", each = \"nmae\" }");
    assert_eq!(message, "`labels.nmae` is not in the spec (no `nmae`)");
}

#[test]
fn a_projection_can_map_a_rest_response_onto_the_event() {
    let fields =
        "rest = \"GET /things/{id}\"\nfields.title = { path = \"title\", rest = \"title\" }";
    let out = generated(fields);
    assert!(out.contains("impl FromRest for Thing"), "{out}");
    assert!(out.contains("(\"/title\", \"/title\")"), "{out}");
}

#[test]
fn a_typo_in_a_rest_path_is_named() {
    let fields =
        "rest = \"GET /things/{id}\"\nfields.title = { path = \"title\", rest = \"titel\" }";
    assert_eq!(error(fields), "`titel` is not in the spec (no `titel`)");
}

#[test]
fn every_field_must_say_where_it_comes_from_in_a_rest_response() {
    let fields = "rest = \"GET /things/{id}\"\nfields.title = { path = \"title\" }";
    assert_eq!(error(fields), "Thing.title has no `rest` path");
}

#[test]
fn an_operation_that_is_not_in_the_spec_is_named() {
    let fields = "rest = \"GET /nope\"\nfields.title = { path = \"title\", rest = \"title\" }";
    assert_eq!(error(fields), "`GET /nope` is not in the spec");
}

#[test]
fn a_webhook_that_is_not_in_the_spec_is_named() {
    let message = generate(&spec(), "[T]\nwebhook = \"nope\"\nkind = \"t\"\n").unwrap_err();
    assert_eq!(message.0, "webhook `nope` is not in the spec");
}
