package catherd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Decode parses JSON keeping numbers exact.
func Decode(b []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	return m, nil
}
func isPlain(v any) bool { _, ok := v.(map[string]any); return ok }

// Clone deep-copies a decoded document.
func Clone(v any) any {
	switch x := v.(type) {
	case map[string]any:
		o := make(map[string]any, len(x))
		for k, c := range x {
			o[k] = Clone(c)
		}
		return o
	case []any:
		o := make([]any, len(x))
		for i, c := range x {
			o[i] = Clone(c)
		}
		return o
	}
	return v
}

// MergePatch is RFC 7396 as catherd applies it: objects merge, null deletes, arrays and scalars replace.
func MergePatch(target, patch any) any {
	p, ok := patch.(map[string]any)
	if !ok {
		return Clone(patch)
	}
	out := map[string]any{}
	if t, ok := target.(map[string]any); ok {
		for k, v := range t {
			out[k] = Clone(v)
		}
	}
	for k, v := range p {
		if v == nil {
			delete(out, k)
		} else {
			out[k] = MergePatch(out[k], v)
		}
	}
	return out
}
func same(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

// PatchBetween is the patch that turns a into b: differing leaves, lists whole, a removed key as null.
func PatchBetween(a, b map[string]any) map[string]any {
	var diff func(x, y any) any
	diff = func(x, y any) any {
		xm, ok1 := x.(map[string]any)
		ym, ok2 := y.(map[string]any)
		if !ok1 || !ok2 {
			return y
		}
		out := map[string]any{}
		for k := range xm {
			if _, ok := ym[k]; !ok {
				out[k] = nil
			}
		}
		for k, v := range ym {
			if old, ok := xm[k]; !ok || !same(old, v) {
				if !ok {
					out[k] = v
				} else {
					out[k] = diff(old, v)
				}
			}
		}
		return out
	}
	return diff(a, b).(map[string]any)
}

// Issue is one finding about a profile document.
type Issue struct {
	Path        []string `bson:"path" json:"path"`
	PathDisplay string   `bson:"path_display" json:"path_display"`
	Code        string   `bson:"code" json:"code"`
	Message     string   `bson:"message" json:"message"`
	Fix         string   `bson:"fix" json:"fix"`
	Origin      string   `bson:"origin" json:"origin"`     // orqen or catherd
	Severity    string   `bson:"severity" json:"severity"` // error or warning
}

func NewIssue(origin, code string, path []string, msg string) Issue {
	return Issue{Path: path, PathDisplay: strings.Join(path, "."), Code: code, Message: msg, Origin: origin, Severity: "error"}
}

// ValidatePatch checks a patch against the 1.5.0 patch schema (strict at every level).
func ValidatePatch(patch map[string]any) []Issue {
	var out []Issue
	bad := func(code string, path []string, msg string) { out = append(out, NewIssue("orqen", code, path, msg)) }
	obj := func(path []string, v any, allowed map[string]func(path []string, v any)) {
		m, ok := v.(map[string]any)
		if !ok {
			bad("bad_value", path, "must be an object")
			return
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			f, ok := allowed[k]
			p := append(append([]string{}, path...), k)
			if !ok {
				bad("unsupported_field", p, "this field cannot be set through catherd's patch interface")
				continue
			}
			f(p, m[k])
		}
	}
	str := func(values []string) func([]string, any) {
		return func(p []string, v any) {
			s, ok := v.(string)
			if !ok || !has(values, s) {
				bad("unknown_value", p, fmt.Sprintf("must be one of %s", strings.Join(values, ", ")))
			}
		}
	}
	boolean := func(nullable bool) func([]string, any) {
		return func(p []string, v any) {
			if _, ok := v.(bool); !ok && !(nullable && v == nil) {
				bad("bad_value", p, "must be true or false")
			}
		}
	}
	positive := func(nullable bool) func([]string, any) {
		return func(p []string, v any) {
			if v == nil && nullable {
				return
			}
			n, ok := v.(json.Number)
			f, err := n.Float64()
			if !ok || err != nil || f <= 0 {
				bad("bad_value", p, "must be a positive number")
			}
		}
	}
	rung := func(nullable bool) func([]string, any) {
		return func(p []string, v any) {
			if v == nil && nullable {
				return
			}
			s, ok := v.(string)
			if _, valid := ParseRung(s); !ok || !valid {
				bad("bad_rung", p, "a rung is <backend>:<model>#<effort>")
			}
		}
	}
	list := func(values []string, grammar bool) func([]string, any) {
		return func(p []string, v any) {
			arr, ok := v.([]any)
			if !ok {
				bad("bad_value", p, "must be a list")
				return
			}
			for i, x := range arr {
				s, ok := x.(string)
				ip := append(append([]string{}, p...), fmt.Sprint(i))
				if grammar {
					if _, valid := ParseRung(s); !ok || !valid {
						bad("bad_rung", ip, "a rung is <backend>:<model>#<effort>")
					}
				} else if !ok || !has(values, s) {
					bad("unknown_value", ip, fmt.Sprintf("must be one of %s", strings.Join(values, ", ")))
				}
			}
		}
	}
	role := func(p []string, v any) {
		timeouts := func(p []string, v any) {
			if v == nil {
				return
			}
			obj(p, v, map[string]func([]string, any){"idleMin": positive(true), "wallMin": positive(true)})
		}
		obj(p, v, map[string]func([]string, any){
			"enabled": boolean(false), "access": str(Access), "rungs": list(nil, true), "defaultRung": rung(true), "network": boolean(true), "timeouts": timeouts,
		})
	}
	roles := map[string]func([]string, any){}
	for _, r := range Roles {
		roles[r] = role
	}
	harness := map[string]func([]string, any){}
	for _, h := range HarnessKeys {
		harness[h] = func(p []string, v any) { obj(p, v, map[string]func([]string, any){"isolated": boolean(false)}) }
	}
	obj(nil, patch, map[string]func([]string, any){
		"objective": str(Objectives),
		"jev":       func(p []string, v any) { obj(p, v, map[string]func([]string, any){"use": str(JevUses)}) },
		"billing": func(p []string, v any) {
			allowed := map[string]func([]string, any){}
			for _, k := range BillingKeys {
				allowed[k] = func(kp []string, x any) {
					if x != nil {
						str(BillingModes)(kp, x)
					}
				}
			}
			obj(p, v, allowed)
		},
		"roles":   func(p []string, v any) { obj(p, v, roles) },
		"harness": func(p []string, v any) { obj(p, v, harness) },
		"failover": func(p []string, v any) {
			m, ok := v.(map[string]any)
			if !ok {
				bad("bad_value", p, "must be an object")
				return
			}
			for k, x := range m {
				kp := append(append([]string{}, p...), k)
				if _, valid := ParseRung(k); !valid {
					bad("bad_rung", kp, "a rung is <backend>:<model>#<effort>")
				}
				rung(true)(kp, x)
			}
		},
		"budget": func(p []string, v any) {
			obj(p, v, map[string]func([]string, any){"minutes": positive(true), "tokens": positive(true), "usd": positive(true)})
		},
		"timeouts": func(p []string, v any) {
			obj(p, v, map[string]func([]string, any){"idleMin": positive(false), "wallMin": positive(false)})
		},
		"preflight": func(p []string, v any) { obj(p, v, map[string]func([]string, any){"confirm": boolean(false)}) },
		"lock": func(p []string, v any) {
			obj(p, v, map[string]func([]string, any){"heavy": func(p []string, v any) {
				if s, ok := v.(string); ok && s == "cpus/2" {
					return
				}
				n, ok := v.(json.Number)
				i, err := n.Int64()
				if !ok || err != nil || i <= 0 {
					bad("bad_value", p, `must be a positive integer or "cpus/2"`)
				}
			}})
		},
		"notify": list(Notify, false),
	})
	return out
}

// Normalize turns json.Number into int64 or float64 so a decoded document can be stored in MongoDB.
func Normalize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		o := make(map[string]any, len(x))
		for k, c := range x {
			o[k] = Normalize(c)
		}
		return o
	case []any:
		o := make([]any, len(x))
		for i, c := range x {
			o[i] = Normalize(c)
		}
		return o
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i
		}
		f, _ := x.Float64()
		return f
	}
	return v
}

// BlankDoc is catherd's built-in default profile document without its name.
func BlankDoc() map[string]any {
	d, _ := Decode(defaultProfileJSON)
	delete(d, "name")
	return d
}
