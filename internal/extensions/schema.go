package extensions

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
)

// The config schema is a small JSON-schema subset (type, enum, minimum, maximum, maxLength, required, properties),
// the same one internal/kindex validates against. It is copied here on purpose: the packages stay independent.

var schemaTypes = map[string]bool{"array": true, "object": true, "string": true, "integer": true, "number": true, "boolean": true}

// ConfigIssue is one violation with the JSON path of the offending value.
type ConfigIssue struct {
	Path string `json:"path"`
	Code string `json:"code"` // type, enum, minimum, maximum, max_length, required, unknown_property
}

// CheckSchema verifies that a manifest's own schema only uses the supported subset.
func CheckSchema(s map[string]any) error {
	if t, ok := s["type"].(string); !ok || !schemaTypes[t] {
		return fmt.Errorf("unsupported or missing type")
	}
	if it, ok := s["items"].(map[string]any); ok {
		if err := CheckSchema(it); err != nil {
			return fmt.Errorf("items: %w", err)
		}
	}
	if props, ok := s["properties"].(map[string]any); ok {
		for k, p := range props {
			ps, ok := p.(map[string]any)
			if !ok {
				return fmt.Errorf("property %q is not an object", k)
			}
			if err := CheckSchema(ps); err != nil {
				return fmt.Errorf("%s: %w", k, err)
			}
		}
	}
	return nil
}

// ValidateConfig checks a config object against the schema and returns every violation, sorted by path. A nil schema
// accepts only an empty config.
func ValidateConfig(schema map[string]any, cfg map[string]any) []ConfigIssue {
	var out []ConfigIssue
	if schema == nil {
		for k := range cfg {
			out = append(out, ConfigIssue{Path: k, Code: "unknown_property"})
		}
	} else {
		check(schema, cfg, "", &out)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Code < out[j].Code
	})
	return out
}

func join(path, k string) string {
	if path == "" {
		return k
	}
	return path + "." + k
}

func check(s map[string]any, v any, path string, out *[]ConfigIssue) {
	add := func(code string) { *out = append(*out, ConfigIssue{Path: path, Code: code}) }
	t, _ := s["type"].(string)
	switch t {
	case "object":
		obj, ok := v.(map[string]any)
		if !ok {
			add("type")
			return
		}
		props, _ := s["properties"].(map[string]any)
		if req, ok := s["required"].([]any); ok {
			for _, r := range req {
				if name, _ := r.(string); name != "" {
					if _, has := obj[name]; !has {
						*out = append(*out, ConfigIssue{Path: join(path, name), Code: "required"})
					}
				}
			}
		}
		for k, val := range obj {
			ps, known := props[k].(map[string]any)
			if !known {
				*out = append(*out, ConfigIssue{Path: join(path, k), Code: "unknown_property"})
				continue
			}
			check(ps, val, join(path, k), out)
		}
		return
	case "array":
		arr, ok := v.([]any)
		if !ok {
			add("type")
			return
		}
		if it, ok := s["items"].(map[string]any); ok {
			for i, e := range arr {
				check(it, e, fmt.Sprintf("%s[%d]", path, i), out)
			}
		}
		if max, ok := s["maxItems"].(float64); ok && float64(len(arr)) > max {
			add("max_items")
		}
		return
	case "string":
		str, ok := v.(string)
		if !ok {
			add("type")
			return
		}
		if max, ok := s["maxLength"].(float64); ok && float64(len([]rune(str))) > max {
			add("max_length")
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			add("type")
			return
		}
	case "integer", "number":
		f, ok := v.(float64)
		if !ok {
			if i, isInt := v.(int); isInt {
				f, ok = float64(i), true
			}
		}
		if !ok || t == "integer" && f != math.Trunc(f) {
			add("type")
			return
		}
		if min, ok := s["minimum"].(float64); ok && f < min {
			add("minimum")
		}
		if max, ok := s["maximum"].(float64); ok && f > max {
			add("maximum")
		}
	}
	if enum, ok := s["enum"].([]any); ok {
		found := false
		for _, e := range enum {
			if e == v {
				found = true
			}
		}
		if !found {
			add("enum")
		}
	}
}

// ValidateJSON checks raw JSON against a schema document of the same subset (plus array items and maxItems). Unknown
// properties are violations, so an output schema is strict. The error names the first violation.
func ValidateJSON(schema []byte, raw []byte) error {
	var s map[string]any
	if err := json.Unmarshal(schema, &s); err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	if err := CheckSchema(s); err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return fmt.Errorf("not JSON: %w", err)
	}
	var out []ConfigIssue
	check(s, v, "$", &out)
	if len(out) == 0 {
		return nil
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return fmt.Errorf("%s: %s", out[0].Path, out[0].Code)
}
