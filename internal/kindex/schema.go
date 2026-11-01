package kindex

import (
	"embed"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

//go:embed schema/v1/*.json
var schemaFS embed.FS

// SchemaVersion labels the contract: assumed, not verified against a real Wiki CLI.
const SchemaVersion = "assumed-v1"

// SchemaNames are the verbs with an output schema (synthesis is the `synthesis register` verb).
var SchemaNames = []string{"version", "ingest", "synthesis", "compile", "lint", "search", "get"}

// SchemaFile returns the embedded schema of a verb.
func SchemaFile(name string) ([]byte, error) { return schemaFS.ReadFile("schema/v1/" + name + ".json") }

// Validate checks a decoded JSON value against the embedded schema subset (type, required, properties,
// additionalProperties, items, enum, minimum). The schemas are plain JSON Schema, so the same files can be fed to any
// other validator.
func Validate(name string, raw []byte) error {
	sb, err := SchemaFile(name)
	if err != nil {
		return err
	}
	var schema map[string]any
	if err = json.Unmarshal(sb, &schema); err != nil {
		return err
	}
	var v any
	if err = json.Unmarshal(raw, &v); err != nil {
		return fmt.Errorf("not JSON: %w", err)
	}
	return check(schema, v, "$")
}

func check(s map[string]any, v any, path string) error {
	if t, ok := s["type"].(string); ok {
		okType := false
		switch t {
		case "object":
			_, okType = v.(map[string]any)
		case "array":
			_, okType = v.([]any)
		case "string":
			_, okType = v.(string)
		case "boolean":
			_, okType = v.(bool)
		case "number":
			_, okType = v.(float64)
		case "integer":
			f, isNum := v.(float64)
			okType = isNum && f == math.Trunc(f)
		}
		if !okType {
			return fmt.Errorf("%s: expected %s", path, t)
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
			return fmt.Errorf("%s: value not in enum", path)
		}
	}
	if min, ok := s["minimum"].(float64); ok {
		if f, isNum := v.(float64); isNum && f < min {
			return fmt.Errorf("%s: below minimum", path)
		}
	}
	if obj, ok := v.(map[string]any); ok {
		props, _ := s["properties"].(map[string]any)
		if req, ok := s["required"].([]any); ok {
			for _, r := range req {
				if _, has := obj[r.(string)]; !has {
					return fmt.Errorf("%s: missing %q", path, r)
				}
			}
		}
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			ps, known := props[k].(map[string]any)
			if !known {
				if ap, ok := s["additionalProperties"].(bool); ok && !ap {
					return fmt.Errorf("%s: unexpected property %q", path, k)
				}
				continue
			}
			if err := check(ps, obj[k], path+"."+k); err != nil {
				return err
			}
		}
	}
	if arr, ok := v.([]any); ok {
		if is, ok := s["items"].(map[string]any); ok {
			for i, e := range arr {
				if err := check(is, e, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// Diff describes the first violation as a structured line for the gated real test.
func Diff(name string, raw []byte) string {
	if err := Validate(name, raw); err != nil {
		return strings.TrimSpace(fmt.Sprintf("schema %s: %v", name, err))
	}
	return ""
}
