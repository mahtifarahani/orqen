package server

import (
	"bytes"
	"context"
	"go.mongodb.org/mongo-driver/v2/bson"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Optional migration acceptance: create real data with the old executable,
// then reopen the same database, artifacts and browser session with Go.
func TestLegacyRustMigration(t *testing.T) {
	binary := os.Getenv("ORQEN_TEST_LEGACY_BINARY")
	if binary == "" {
		t.Skip("set ORQEN_TEST_LEGACY_BINARY to an existing Rust executable")
	}
	f := newTestApp(t, false)
	c := f.a.Config
	f.stop()
	if err := <-f.done; err != nil {
		t.Fatal(err)
	}
	processCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(processCtx, binary, "serve")
	cmd.Env = append(os.Environ(), "ORQEN_PORT="+strconv.Itoa(c.Port), "ORQEN_MONGO_URI="+c.MongoURI, "ORQEN_DB_NAME="+c.DBName, "ORQEN_DATA_DIR="+c.DataDir, "ORQEN_CREDENTIAL_STORE=memory", "ORQEN_DEV=false")
	var logs bytes.Buffer
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	running := true
	defer func() {
		if running {
			cancel()
			_ = cmd.Wait()
		}
	}()
	ready := false
	for i := 0; i < 200; i++ {
		resp, err := f.client.Get(f.url + "/api/v1/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				ready = true
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ready {
		cancel()
		_ = cmd.Wait()
		running = false
		t.Fatal(logs.String())
	}
	data, err := os.ReadFile(filepath.Join(c.DataDir, "bootstrap-url.txt"))
	if err != nil {
		t.Fatal(err)
	}
	_, token, _ := strings.Cut(strings.TrimSpace(string(data)), "#token=")
	claim := f.json("POST", "/api/v1/auth/bootstrap", bson.M{"token": token}, nil, 200)
	f.csrf = claim["csrf_token"].(string)
	body := CreateOrgRequest{"legacy", "Legacy", "en", "UTC"}
	org := f.json("POST", "/api/v1/orgs", body, map[string]string{"Idempotency-Key": "rust-org"}, 201)
	id := org["id"].(string)
	connBody := CreateConnectionRequest{Name: "Jira", Provider: "jira", Deployment: "server", BaseURL: "http://127.0.0.1:1/jira", AuthMethod: "personal_access_token"}
	conn := f.json("POST", "/api/v1/orgs/"+id+"/connections", connBody, map[string]string{"Idempotency-Key": "rust-connection"}, 201)
	conID := conn["id"].(string)
	op := f.startTest(id, conID, "rust-test")
	finished := f.waitOperation(id, op["id"].(string))
	art := finished["result"].(map[string]any)["artifact_id"].(string)
	resp := f.request("GET", "/api/v1/orgs/"+id+"/artifacts/"+art+"/content", nil, nil)
	report, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	events := f.json("GET", "/api/v1/orgs/"+id+"/events", nil, nil, 200)
	last := int64(events["last_seq"].(float64))
	cancel()
	_ = cmd.Wait()
	running = false
	f.start(c, true)
	status := f.json("GET", "/api/v1/auth/status", nil, nil, 200)
	if status["authenticated"] != true || status["csrf_token"] != f.csrf {
		t.Fatal(status)
	}
	resp = f.request("POST", "/api/v1/orgs", body, map[string]string{"Idempotency-Key": "rust-org"})
	if resp.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatal(resp.Header)
	}
	replayed := readResponse(t, resp, 201)
	if !reflect.DeepEqual(replayed, org) {
		t.Fatal(org, replayed)
	}
	resp = f.request("POST", "/api/v1/orgs/"+id+"/connections", connBody, map[string]string{"Idempotency-Key": "rust-connection"})
	if resp.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatal(resp.Header)
	}
	replayed = readResponse(t, resp, 201)
	if !reflect.DeepEqual(replayed, conn) {
		t.Fatal(conn, replayed)
	}
	resp = f.request("GET", "/api/v1/orgs/"+id+"/artifacts/"+art+"/content", nil, nil)
	reportAfter, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(report, reportAfter) {
		t.Fatal("legacy report changed")
	}
	f.json("PATCH", "/api/v1/orgs/"+id, bson.M{"name": "Go"}, map[string]string{"If-Match": "1"}, 200)
	evs, err := f.a.fetchEvents(context.Background(), id, last, 10)
	if err != nil || len(evs) != 1 || evs[0].Seq != last+1 {
		t.Fatal(evs, err)
	}
}
