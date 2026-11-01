package server

import (
	"go.mongodb.org/mongo-driver/v2/bson"
	"strings"
	"testing"
)

func TestPinnedRulesCRUD(t *testing.T) {
	e := newIntakeEnv(t, intakeOpts{NoEnable: true})
	base := e.orgPath("rules")
	resp := e.f.request("POST", base, CreatePinnedRuleRequest{Scope: "org", Title: " Be careful ", Body: " Never log tokens. ", Critical: true}, nil)
	if resp.Header.Get("ETag") != `"1"` || resp.Header.Get("Location") == "" {
		t.Fatal(resp.Header)
	}
	org := readResponse(t, resp, 201)
	if org["title"] != "Be careful" || org["body"] != "Never log tokens." || org["critical"] != true || org["scope"] != "org" || org["project_id"] != nil {
		t.Fatal(org)
	}
	prj := e.f.json("POST", base, CreatePinnedRuleRequest{Scope: "project", ProjectID: ptr(e.pid), Title: "Project rule", Body: "Use pnpm."}, nil, 201)
	list := decodeArray(t, e.f.request("GET", base, nil, nil))
	if len(list) != 2 || list[0]["scope"] != "org" {
		t.Fatal(list)
	}
	if got := decodeArray(t, e.f.request("GET", base+"?scope=project&project_id="+e.pid, nil, nil)); len(got) != 1 || got[0]["id"] != prj["id"] {
		t.Fatal(got)
	}
	id := org["id"].(string)
	patched := e.f.json("PATCH", base+"/"+id, PatchPinnedRuleRequest{Critical: ptr(false), Title: ptr("Careful")}, map[string]string{"If-Match": "1"}, 200)
	if patched["revision"] != float64(2) || patched["critical"] != false || patched["title"] != "Careful" {
		t.Fatal(patched)
	}
	conflict := e.f.json("PATCH", base+"/"+id, PatchPinnedRuleRequest{Body: ptr("x")}, map[string]string{"If-Match": "1"}, 409)
	expectCode(t, conflict, "revision_conflict")
	if conflict["current"].(map[string]any)["revision"] != float64(2) {
		t.Fatal(conflict)
	}
	expectCode(t, e.f.json("PATCH", base+"/"+id, PatchPinnedRuleRequest{}, map[string]string{"If-Match": "2"}, 422), "validation_failed")
	for _, bad := range []CreatePinnedRuleRequest{
		{Scope: "org", Title: "", Body: "x"}, {Scope: "org", Title: "t", Body: "  "}, {Scope: "org", Title: strings.Repeat("a", 201), Body: "x"}, {Scope: "org", Title: "t", Body: strings.Repeat("b", 16*1024+1)},
		{Scope: "org", ProjectID: ptr(e.pid), Title: "t", Body: "x"}, {Scope: "project", Title: "t", Body: "x"}, {Scope: "project", ProjectID: ptr(newID("prj")), Title: "t", Body: "x"}, {Scope: "team", Title: "t", Body: "x"},
	} {
		expectCode(t, e.f.json("POST", base, bad, nil, 422), "validation_failed")
	}
	e.f.json("DELETE", base+"/"+id, nil, map[string]string{"If-Match": "2"}, 204)
	if got := decodeArray(t, e.f.request("GET", base, nil, nil)); len(got) != 1 {
		t.Fatal(got)
	}
	e.f.json("PATCH", base+"/"+id, PatchPinnedRuleRequest{Title: ptr("again")}, map[string]string{"If-Match": "3"}, 404)
	// Events carry ids and flags, never rule text.
	events := e.f.json("GET", e.orgPath("events")+"?limit=500", nil, nil, 200)
	for _, x := range events["items"].([]any) {
		m := x.(map[string]any)
		if strings.HasPrefix(m["type"].(string), "pinned_rule.") && (strings.Contains(toJSON(m["payload"]), "tokens") || strings.Contains(toJSON(m["payload"]), "pnpm")) {
			t.Fatal("rule text in an event", m)
		}
	}
	if e.count("events", bson.M{"org_id": e.org, "type": bson.M{"$regex": "^pinned_rule\\."}}) != 4 {
		t.Fatal("events")
	}
}
