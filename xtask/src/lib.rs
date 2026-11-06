//! Generates event types from a projection: a declared list of fields, checked against a pinned
//! spec. A spec describes hundreds of fields and workflows need about ten, so types are
//! projected, not generated whole. A string is `Untrusted` unless the projection names a
//! validated type, because the spec types a title and a commit hash alike.
use serde_json::Value;
use std::fmt;

#[derive(Debug, PartialEq)]
pub struct GenError(pub String);

impl fmt::Display for GenError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.0)
    }
}

fn fail<T>(message: impl Into<String>) -> Result<T, GenError> {
    Err(GenError(message.into()))
}

fn resolve<'a>(spec: &'a Value, schema: &'a Value) -> Result<&'a Value, GenError> {
    match schema["$ref"].as_str() {
        None => Ok(schema),
        Some(reference) => match spec.pointer(reference.trim_start_matches('#')) {
            Some(found) => resolve(spec, found),
            None => fail(format!("the spec has a broken reference: {reference}")),
        },
    }
}

/// A schema and everything it composes.
fn variants<'a>(spec: &'a Value, schema: &'a Value) -> Result<Vec<&'a Value>, GenError> {
    let schema = resolve(spec, schema)?;
    let mut out = vec![schema];
    for key in ["allOf", "anyOf", "oneOf"] {
        for part in schema[key].as_array().into_iter().flatten() {
            out.extend(variants(spec, part)?);
        }
    }
    Ok(out)
}

/// The schemas at a dotted path, or an error naming the path and the part that does not exist.
fn at<'a>(spec: &'a Value, root: &'a Value, path: &str) -> Result<Vec<&'a Value>, GenError> {
    let mut current = vec![root];
    for part in path.split('.') {
        let mut next = vec![];
        for schema in current {
            for variant in variants(spec, schema)? {
                if let Some(found) = variant["properties"].get(part) {
                    next.push(found);
                }
                if let Some(found) = variant["items"]["properties"].get(part) {
                    next.push(found);
                }
                if variant.get("items").is_some() {
                    for item in variants(spec, &variant["items"])? {
                        if let Some(found) = item["properties"].get(part) {
                            next.push(found);
                        }
                    }
                }
            }
        }
        if next.is_empty() {
            return fail(format!("`{path}` is not in the spec (no `{part}`)"));
        }
        current = next;
    }
    Ok(current)
}

/// What a field's schemas amount to.
struct Leaf {
    kind: String,
    nullable: bool,
    values: Vec<String>,
}

fn leaf(spec: &Value, schemas: &[&Value], path: &str) -> Result<Leaf, GenError> {
    let mut kind = None;
    let mut nullable = false;
    let mut values = vec![];
    for schema in schemas {
        for variant in variants(spec, schema)? {
            nullable |= variant["nullable"].as_bool().unwrap_or(false);
            let types = match &variant["type"] {
                Value::String(t) => vec![t.as_str()],
                Value::Array(ts) => ts.iter().filter_map(Value::as_str).collect(),
                _ => vec![],
            };
            nullable |= types.contains(&"null");
            let concrete = types.into_iter().find(|t| *t != "null");
            kind = kind.or(concrete.map(str::to_string));
            for value in variant["enum"].as_array().into_iter().flatten() {
                match value.as_str() {
                    Some(text) if !values.iter().any(|v| v == text) => {
                        values.push(text.to_string());
                    }
                    Some(_) => {}
                    None => nullable = true,
                }
            }
        }
    }
    match kind {
        Some(kind) => Ok(Leaf {
            kind,
            nullable,
            values,
        }),
        None => fail(format!("`{path}` has no type in the spec")),
    }
}

fn camel(text: &str) -> String {
    text.split(['_', '-'])
        .map(|part| {
            let mut chars = part.chars();
            chars
                .next()
                .map_or(String::new(), |c| c.to_uppercase().chain(chars).collect())
        })
        .collect()
}

fn pointer(path: &str) -> String {
    format!("/{}", path.replace('.', "/"))
}

struct Field {
    ty: String,
    init: String,
    rest: Option<(String, String)>,
    enumeration: Option<String>,
}

fn enumeration(name: &str, values: &[String]) -> String {
    let variants: String = values
        .iter()
        .map(|v| format!("        #[serde(rename = \"{v}\")] {},\n", camel(v)))
        .collect();
    format!(
        "clean! {{\n    pub enum {name} {{\n{variants}        #[serde(other)] Unknown,\n    }}\n}}\n"
    )
}

fn field(
    spec: &Value,
    root: &Value,
    type_name: &str,
    name: &str,
    declared: &toml::Value,
) -> Result<Field, GenError> {
    let text = |key: &str| declared.get(key).and_then(toml::Value::as_str);
    let Some(path) = text("path") else {
        return fail(format!("{type_name}.{name} has no `path`"));
    };
    let ptr = pointer(path);
    let schemas = at(spec, root, path)?;
    let mut enumerated = None;
    let (ty, init) = if let Some(each) = text("each") {
        let full = format!("{path}.{each}");
        let found = leaf(spec, &at(spec, root, &full)?, &full)?;
        let item = pointer(each);
        if found.kind == "integer" {
            (
                "Vec<u64>".to_string(),
                format!("p.list(\"{ptr}\", \"{item}\")?"),
            )
        } else {
            (
                "Untrusted<Vec<String>>".to_string(),
                format!("p.text_list(\"{ptr}\", \"{item}\")?"),
            )
        }
    } else {
        let found = leaf(spec, &schemas, path)?;
        let optional = |ty: String| match found.nullable {
            true => format!("Option<{ty}>"),
            false => ty,
        };
        let read = format!("p.field(\"{ptr}\")?");
        if let Some(validated) = text("as") {
            if found.kind != "string" {
                return fail(format!(
                    "`{path}` is {}, cannot be `as {validated}`",
                    found.kind
                ));
            }
            (optional(validated.to_string()), read)
        } else if !found.values.is_empty() {
            let enum_name = format!("{type_name}{}", camel(name));
            enumerated = Some(enumeration(&enum_name, &found.values));
            (optional(enum_name), read)
        } else if found.kind == "integer" {
            (optional("u64".to_string()), read)
        } else if found.kind == "boolean" {
            (optional("bool".to_string()), read)
        } else if found.nullable {
            (
                "Untrusted<Option<String>>".to_string(),
                format!("p.optional_text(\"{ptr}\")?"),
            )
        } else {
            (
                "Untrusted<String>".to_string(),
                format!("p.text(\"{ptr}\")?"),
            )
        }
    };
    Ok(Field {
        ty,
        init,
        rest: text("rest").map(|rest| (rest.to_string(), ptr)),
        enumeration: enumerated,
    })
}

/// The response schema of an operation such as `GET /repos/{owner}/{repo}`.
fn operation<'a>(spec: &'a Value, declared: &str) -> Result<&'a Value, GenError> {
    let Some((method, path)) = declared.split_once(' ') else {
        return fail(format!("`{declared}` is not `METHOD /path`"));
    };
    let method = method.to_lowercase();
    let schema = &spec["paths"][path][method.as_str()]["responses"]["200"]["content"]["application/json"]
        ["schema"];
    if schema.is_null() {
        return fail(format!("`{declared}` is not in the spec"));
    }
    Ok(schema)
}

fn event(spec: &Value, name: &str, declared: &toml::Value) -> Result<String, GenError> {
    let text = |key: &str| declared.get(key).and_then(toml::Value::as_str);
    let (Some(webhook), Some(kind)) = (text("webhook"), text("kind")) else {
        return fail(format!("{name} needs `webhook` and `kind`"));
    };
    let root = &spec["x-webhooks"][webhook]["post"]["requestBody"]["content"]["application/json"]["schema"];
    if root.is_null() {
        return fail(format!("webhook `{webhook}` is not in the spec"));
    }
    let Some(declared_fields) = declared.get("fields").and_then(toml::Value::as_table) else {
        return fail(format!("{name} has no fields"));
    };
    let mut enums = String::new();
    let mut fields = String::new();
    let mut inits = String::new();
    let mut rest_pairs = String::new();
    for (field_name, f) in declared_fields {
        let generated = field(spec, root, name, field_name, f)?;
        enums.push_str(&generated.enumeration.unwrap_or_default());
        fields.push_str(&format!("        pub {field_name}: {},\n", generated.ty));
        inits.push_str(&format!("            {field_name}: {},\n", generated.init));
        if let Some((rest, webhook_pointer)) = generated.rest {
            rest_pairs.push_str(&format!(
                "        (\"{}\", \"{webhook_pointer}\"),\n",
                pointer(&rest)
            ));
        }
    }
    let mut out = format!(
        "{enums}\nobserved! {{\n    pub struct {name} {{\n{fields}    }}\n}}\n\n\
         impl Event for {name} {{\n    const KIND: &'static str = \"{kind}\";\n    \
         fn parse(p: &Payload<'_>) -> Result<Self, Malformed> {{\n        Ok(Self {{\n{inits}        \
         }})\n    }}\n}}\n"
    );
    if let Some(rest) = text("rest") {
        let response = operation(spec, rest)?;
        for (field_name, f) in declared_fields {
            let Some(path) = f.get("rest").and_then(toml::Value::as_str) else {
                return fail(format!("{name}.{field_name} has no `rest` path"));
            };
            at(spec, response, path)?;
        }
        out.push_str(&format!(
            "\nimpl FromRest for {name} {{\n    const OPERATION: &'static str = \"{rest}\";\n    \
             const FIELDS: &'static [(&'static str, &'static str)] = &[\n{rest_pairs}    ];\n}}\n"
        ));
    }
    Ok(out)
}

/// Rust source for a projection. Unformatted: the caller runs rustfmt.
pub fn generate(spec: &Value, projection: &str) -> Result<String, GenError> {
    let table: toml::Table = projection
        .parse()
        .map_err(|e| GenError(format!("the projection is not valid TOML: {e}")))?;
    let mut out =
        "// @generated by `cargo xtask generate`. Do not edit.\nuse almanac::prelude::*;\n"
            .to_string();
    for (name, declared) in &table {
        out.push('\n');
        out.push_str(&event(spec, name, declared)?);
    }
    Ok(out)
}
