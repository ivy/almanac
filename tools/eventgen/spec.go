package main

import (
	"fmt"
	"slices"
	"strings"
)

// spec is an OpenAPI document decoded as JSON, walked by the generator.
type spec struct {
	root any
}

// object is a JSON object, or nil when value is something else.
func object(value any) map[string]any {
	found, _ := value.(map[string]any)
	return found
}

// resolve follows a schema's $ref until it reaches one that has none.
func (s spec) resolve(schema any) (any, error) {
	for {
		reference, ok := object(schema)["$ref"].(string)
		if !ok {
			return schema, nil
		}
		found, ok := pointer(s.root, strings.TrimPrefix(reference, "#"))
		if !ok {
			return nil, fmt.Errorf("the spec has a broken reference: %s", reference)
		}
		schema = found
	}
}

// variants is a schema and everything it composes with allOf, anyOf and oneOf.
func (s spec) variants(schema any) ([]map[string]any, error) {
	resolved, err := s.resolve(schema)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{object(resolved)}
	for _, key := range []string{"allOf", "anyOf", "oneOf"} {
		parts, _ := object(resolved)[key].([]any)
		for _, part := range parts {
			more, err := s.variants(part)
			if err != nil {
				return nil, err
			}
			out = append(out, more...)
		}
	}
	return out, nil
}

// at is the schemas found at a dotted path below root, looking through lists to their items.
// A path that does not exist is an error naming the path and the part that is missing.
func (s spec) at(root any, path string) ([]any, error) {
	current := []any{root}
	for part := range strings.SplitSeq(path, ".") {
		var next []any
		for _, schema := range current {
			variants, err := s.variants(schema)
			if err != nil {
				return nil, err
			}
			for _, variant := range variants {
				if found, ok := object(variant["properties"])[part]; ok {
					next = append(next, found)
				}
				items, ok := variant["items"]
				if !ok {
					continue
				}
				itemVariants, err := s.variants(items)
				if err != nil {
					return nil, err
				}
				for _, item := range itemVariants {
					if found, ok := object(item["properties"])[part]; ok {
						next = append(next, found)
					}
				}
			}
		}
		if len(next) == 0 {
			return nil, fmt.Errorf("`%s` is not in the spec (no `%s`)", path, part)
		}
		current = next
	}
	return current, nil
}

// leaf is what the schemas of one field amount to.
type leaf struct {
	// kind is the JSON Schema type, such as "string" or "integer".
	kind     string
	nullable bool
	// values are the strings an enumerated field may hold, in the spec's order.
	values []string
}

func (s spec) leaf(schemas []any, path string) (leaf, error) {
	var found leaf
	for _, schema := range schemas {
		variants, err := s.variants(schema)
		if err != nil {
			return leaf{}, err
		}
		for _, variant := range variants {
			if nullable, _ := variant["nullable"].(bool); nullable {
				found.nullable = true
			}
			var types []string
			switch declared := variant["type"].(type) {
			case string:
				types = []string{declared}
			case []any:
				for _, t := range declared {
					if name, ok := t.(string); ok {
						types = append(types, name)
					}
				}
			}
			for _, t := range types {
				if t == "null" {
					found.nullable = true
				} else if found.kind == "" {
					found.kind = t
				}
			}
			values, _ := variant["enum"].([]any)
			for _, value := range values {
				text, ok := value.(string)
				switch {
				case !ok:
					found.nullable = true
				case !slices.Contains(found.values, text):
					found.values = append(found.values, text)
				}
			}
		}
	}
	if found.kind == "" {
		return leaf{}, fmt.Errorf("`%s` has no type in the spec", path)
	}
	return found, nil
}

// operation is the response schema of an operation such as "GET /repos/{owner}/{repo}".
func (s spec) operation(declared string) (any, error) {
	method, path, ok := strings.Cut(declared, " ")
	if !ok {
		return nil, fmt.Errorf("`%s` is not `METHOD /path`", declared)
	}
	schema := object(object(object(object(object(object(object(object(s.root)["paths"])[path])[strings.ToLower(method)])["responses"])["200"])["content"])["application/json"])["schema"]
	if schema == nil {
		return nil, fmt.Errorf("`%s` is not in the spec", declared)
	}
	return schema, nil
}

// webhook is the payload schema of a webhook such as "pull-request-opened".
func (s spec) webhook(name string) (any, error) {
	schema := object(object(object(object(object(object(object(s.root)["x-webhooks"])[name])["post"])["requestBody"])["content"])["application/json"])["schema"]
	if schema == nil {
		return nil, fmt.Errorf("webhook `%s` is not in the spec", name)
	}
	return schema, nil
}

// unescape undoes a JSON pointer's escapes for '/' and '~'.
var unescape = strings.NewReplacer("~1", "/", "~0", "~")

// pointer finds the value at a JSON pointer (RFC 6901) into objects.
func pointer(root any, at string) (any, bool) {
	if at == "" {
		return root, true
	}
	current := root
	for token := range strings.SplitSeq(strings.TrimPrefix(at, "/"), "/") {
		next, ok := object(current)[unescape.Replace(token)]
		if !ok {
			return nil, false
		}
		current = next
	}
	return current, true
}
