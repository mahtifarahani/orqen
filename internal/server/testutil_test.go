package server

import (
	"encoding/json"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"net/http"
	"strings"
	"testing"
	"time"
)

type connSpec struct {
	Name, Provider, Deployment, Base, Method, Username, Secret string
	AllowedHosts                                               []string
}

// providerConn creates a connection and stores its credential; it returns the connection id.
func (f *testApp) providerConn(org string, c connSpec) string {
	f.t.Helper()
	body := CreateConnectionRequest{Name: c.Name, Provider: c.Provider, Deployment: c.Deployment, BaseURL: c.Base, AuthMethod: c.Method}
	if c.Username != "" {
		body.AuthUsername = ptr(c.Username)
	}
	if len(c.AllowedHosts) > 0 {
		body.AllowedHosts = &c.AllowedHosts
	}
	id := f.json("POST", "/api/v1/orgs/"+org+"/connections", body, nil, 201)["id"].(string)
	if c.Secret != "" {
		f.json("PUT", connectionPath(org, id)+"/credential", bson.M{"secret": c.Secret}, map[string]string{"If-Match": "1"}, 200)
	}
	return id
}

var testKeyCounter int

// runTest starts a connection test and waits; it returns the finished operation.
func (f *testApp) runTest(org, id string) map[string]any {
	f.t.Helper()
	testKeyCounter++
	op := f.startTest(org, id, fmt.Sprintf("test-%d", testKeyCounter))
	return f.waitOperation(org, op["id"].(string))
}

// stages returns the artifact stages keyed by name.
func (f *testApp) stages(org string, op map[string]any) map[string]map[string]any {
	f.t.Helper()
	art := op["result"].(map[string]any)["artifact_id"].(string)
	report := f.json("GET", "/api/v1/orgs/"+org+"/artifacts/"+art+"/content", nil, nil, 200)
	out := map[string]map[string]any{}
	for _, x := range report["stages"].([]any) {
		s := x.(map[string]any)
		out[s["stage"].(string)] = s
	}
	return out
}
func requireStage(t *testing.T, stages map[string]map[string]any, name, status, code string) {
	t.Helper()
	s := stages[name]
	c, _ := s["code"].(string)
	if s["status"] != status || c != code {
		t.Fatalf("stage %s: %v", name, strings.TrimSpace(fmt.Sprint(s)))
	}
}

func decodeBody(resp *http.Response, v any) error {
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}

// waitOperationDone waits until no job of the organization is active.
func (f *testApp) waitOperationDone(org string) {
	f.t.Helper()
	for i := 0; i < 400; i++ {
		n, _ := f.db.Coll("jobs").CountDocuments(f.t.Context(), bson.M{"org_id": org, "active": true})
		if n == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	f.t.Fatal("jobs did not finish")
}
