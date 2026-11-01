package codexhost

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const schemaDir = "testdata/schema-0.160.0"

func readJSON(t *testing.T, rel string) map[string]any {
	b, err := os.ReadFile(filepath.Join(schemaDir, rel))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err = json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// methodsOf lists the "method" enum value of each oneOf branch.
func methodsOf(t *testing.T, file string) map[string]bool {
	out := map[string]bool{}
	for _, b := range readJSON(t, file)["oneOf"].([]any) {
		props := b.(map[string]any)["properties"].(map[string]any)
		if m, ok := props["method"].(map[string]any); ok {
			out[m["enum"].([]any)[0].(string)] = true
		}
	}
	return out
}

func TestEveryMethodExistsInThePinnedSchema(t *testing.T) {
	if v, _ := os.ReadFile(filepath.Join(schemaDir, "VERSION")); !strings.HasPrefix(string(v), "codex-cli "+ProtocolVersion) {
		t.Fatalf("schema VERSION does not match ProtocolVersion %s: %q", ProtocolVersion, v)
	}
	for file, names := range map[string][]string{
		"ClientRequest.json":      ClientRequests,
		"ClientNotification.json": ClientNotifications,
		"ServerNotification.json": ServerNotifications,
		"ServerRequest.json":      ServerRequestMethods,
	} {
		have := methodsOf(t, file)
		for _, n := range names {
			if !have[n] {
				t.Errorf("%s: method %q does not exist in the pinned schema", file, n)
			}
		}
	}
}

// jsonFields returns the wire names of a struct and which of them are always sent.
func jsonFields(typ reflect.Type) (all, alwaysSent map[string]bool) {
	all, alwaysSent = map[string]bool{}, map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		name, opts, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			continue
		}
		all[name] = true
		if !strings.Contains(opts, "omitempty") {
			alwaysSent[name] = true
		}
	}
	return
}

func TestWireParamsMatchTheSchema(t *testing.T) {
	cases := []struct {
		file string
		typ  reflect.Type
	}{
		{"v1/InitializeParams.json", reflect.TypeOf(initializeParams{})},
		{"v2/ThreadStartParams.json", reflect.TypeOf(threadStartParams{})},
		{"v2/ThreadResumeParams.json", reflect.TypeOf(threadResumeParams{})},
		{"v2/TurnStartParams.json", reflect.TypeOf(turnStartParams{})},
		{"v2/TurnInterruptParams.json", reflect.TypeOf(turnInterruptParams{})},
		{"v2/GetAccountParams.json", reflect.TypeOf(accountReadParams{})},
		{"v2/ModelListParams.json", reflect.TypeOf(modelListParams{})},
		{"v2/ListMcpServerStatusParams.json", reflect.TypeOf(mcpStatusParams{})},
	}
	for _, c := range cases {
		schema := readJSON(t, c.file)
		props, _ := schema["properties"].(map[string]any)
		all, always := jsonFields(c.typ)
		for f := range all {
			if _, ok := props[f]; !ok {
				t.Errorf("%s: field %q is sent by Orqen but unknown to the schema", c.file, f)
			}
		}
		if req, ok := schema["required"].([]any); ok {
			for _, r := range req {
				if !always[r.(string)] {
					t.Errorf("%s: required field %q is not always sent by Orqen", c.file, r)
				}
			}
		}
	}
	// the user input item and the effort/approval/sandbox enums Orqen sends
	ui := readJSON(t, "v2/TurnStartParams.json")["definitions"].(map[string]any)["UserInput"].(map[string]any)["oneOf"].([]any)[0].(map[string]any)
	if ui["properties"].(map[string]any)["type"].(map[string]any)["enum"].([]any)[0] != "text" {
		t.Fatal("text input")
	}
	approval := readJSON(t, "v2/ThreadStartParams.json")["definitions"].(map[string]any)["AskForApproval"].(map[string]any)["oneOf"].([]any)[0].(map[string]any)["enum"]
	if !contains(approval, "never") {
		t.Fatal("approval policy never")
	}
	sandbox := readJSON(t, "v2/ThreadStartParams.json")["definitions"].(map[string]any)["SandboxMode"].(map[string]any)["enum"]
	if !contains(sandbox, "read-only") {
		t.Fatal("sandbox read-only")
	}
}

func contains(list any, v string) bool {
	for _, x := range list.([]any) {
		if x == v {
			return true
		}
	}
	return false
}

func TestItemAndResultFieldsExist(t *testing.T) {
	item := readJSON(t, "v2/TurnStartResponse.json")["definitions"].(map[string]any)["ThreadItem"].(map[string]any)["oneOf"].([]any)
	for _, b := range item {
		props := b.(map[string]any)["properties"].(map[string]any)
		if props["type"].(map[string]any)["enum"].([]any)[0] == "mcpToolCall" {
			for _, f := range []string{"server", "tool", "arguments", "status", "result", "error"} {
				if _, ok := props[f]; !ok {
					t.Errorf("mcpToolCall.%s missing", f)
				}
			}
			return
		}
	}
	t.Fatal("mcpToolCall item type missing from the schema")
}
