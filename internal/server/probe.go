package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"net"
	"net/http"
	"net/url"
	"orqen/internal/providers"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func stage(name, status, code, detail string) StageResult {
	var c *string
	if code != "" {
		c = &code
	}
	return StageResult{Stage: name, Status: status, Code: c, Detail: detail}
}
func overallStatus(stages []StageResult) string {
	failed := func(name string) bool {
		for _, r := range stages {
			if r.Stage == name && r.Status == "failed" {
				return true
			}
		}
		return false
	}
	if failed("network") || failed("tls") || failed("http") {
		return "unreachable"
	}
	if failed("credential") || failed("auth") || failed("identity") || failed("capabilities") || failed("sample_access") {
		return "degraded"
	}
	verified := len(stages) == 8
	for _, r := range stages {
		if r.Status != "passed" && !(r.Stage == "tls" && r.Status == "skipped") {
			verified = false
		}
	}
	if verified {
		return "verified"
	}
	return "reachable"
}
func portOf(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	switch u.Scheme {
	case "https":
		return "443"
	case "socks5", "socks5h":
		return "1080"
	default:
		return "80"
	}
}
func hostAllowed(allowed []string, u *url.URL) bool {
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return contains(allowed, host) || contains(allowed, host+":"+portOf(u))
}
func networkStage(ctx context.Context, conn ConnectionDoc) StageResult {
	target := conn.BaseURL
	via := ""
	if conn.ProxyURL != nil {
		target = *conn.ProxyURL
		via = " (via proxy)"
	}
	u, err := url.Parse(target)
	if err != nil || u.Hostname() == "" {
		return stage("network", "failed", "invalid_url", "URL has no host")
	}
	timeout := time.Duration(conn.TimeoutMS) * time.Millisecond
	lookupCtx, cancel := context.WithTimeout(ctx, timeout)
	ips, err := net.DefaultResolver.LookupIPAddr(lookupCtx, u.Hostname())
	cancel()
	if err != nil {
		code := "dns_failed"
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			code = "timeout"
		}
		return stage("network", "failed", code, "network_unavailable: cannot resolve "+u.Hostname()+": "+err.Error())
	}
	last := stage("network", "failed", "dns_failed", "network_unavailable: no addresses found")
	for _, ip := range ips {
		addr := net.JoinHostPort(ip.String(), portOf(u))
		dialer := net.Dialer{Timeout: timeout}
		c, e := dialer.DialContext(ctx, "tcp", addr)
		if e == nil {
			_ = c.Close()
			return stage("network", "passed", "", "TCP connection to "+addr+" succeeded"+via)
		}
		code := "connect_failed"
		var ne net.Error
		if errors.Is(e, syscall.ECONNREFUSED) {
			code = "connection_refused"
		} else if errors.As(e, &ne) && ne.Timeout() {
			code = "timeout"
		}
		last = stage("network", "failed", code, "network_unavailable: "+addr+": "+e.Error())
		if ctx.Err() != nil {
			break
		}
	}
	return last
}
func probeClient(conn ConnectionDoc) (*http.Client, error) {
	o := providers.Options{Timeout: time.Duration(conn.TimeoutMS) * time.Millisecond}
	if conn.ProxyURL != nil {
		o.ProxyURL = *conn.ProxyURL
	}
	if conn.CustomCaPEM != nil {
		o.CustomCAPEM = *conn.CustomCaPEM
	}
	return providers.NewClient(o)
}
func classifyNetwork(err error) (string, string) {
	lower := strings.ToLower(err.Error())
	for _, s := range []string{"certificate", "tls", "handshake", "ssl"} {
		if strings.Contains(lower, s) {
			return "tls_error", err.Error()
		}
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout", err.Error()
	}
	if strings.Contains(lower, "too many redirects") {
		return "too_many_redirects", err.Error()
	}
	var op *net.OpError
	if errors.As(err, &op) {
		return "connect_failed", err.Error()
	}
	return "request_failed", err.Error()
}
func httpStage(ctx context.Context, conn ConnectionDoc, name string) StageResult {
	if name == "tls" && !strings.HasPrefix(conn.BaseURL, "https://") {
		return stage(name, "skipped", "", "plain http")
	}
	client, err := probeClient(conn)
	if err != nil {
		return stage(name, "failed", "client_error", err.Error())
	}
	defer client.CloseIdleConnections()
	escaped := ""
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if name == "tls" {
			return http.ErrUseLastResponse
		}
		if len(via) >= 5 {
			return fmt.Errorf("too many redirects")
		}
		if !hostAllowed(conn.AllowedHosts, req.URL) {
			escaped = req.URL.Hostname()
			return http.ErrUseLastResponse
		}
		return nil
	}
	var req *http.Request
	if name == "tls" {
		req, err = http.NewRequestWithContext(ctx, http.MethodHead, conn.BaseURL, nil)
	} else {
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, conn.BaseURL, nil)
	}
	if err != nil {
		return stage(name, "failed", "invalid_url", err.Error())
	}
	req.Header.Set("User-Agent", "orqen/"+Version)
	resp, err := client.Do(req)
	if err != nil {
		code, detail := classifyNetwork(err)
		return stage(name, "failed", code, detail)
	}
	defer resp.Body.Close()
	if escaped != "" {
		return stage(name, "failed", "redirect_out_of_scope", fmt.Sprintf("HTTP %d redirects to %s, which is not in the allowed hosts", resp.StatusCode, escaped))
	}
	if name == "tls" {
		return stage(name, "passed", "", "TLS handshake succeeded")
	}
	return stage(name, "passed", "", fmt.Sprintf("HTTP %d", resp.StatusCode))
}
func (a *App) runStage(ctx context.Context, name string, conn ConnectionDoc, done []StageResult, st *adapterState) StageResult {
	failed := func(name string) bool {
		for _, r := range done {
			if r.Stage == name && r.Status == "failed" {
				return true
			}
		}
		return false
	}
	switch name {
	case "network":
		return networkStage(ctx, conn)
	case "tls", "http":
		if failed("network") || name == "http" && failed("tls") {
			return stage(name, "skipped", "", "previous stage failed")
		}
		return httpStage(ctx, conn, name)
	case "credential":
		if conn.CredentialRef == nil || conn.CredentialState != "set" {
			return stage(name, "skipped", "no_credential", "no credential is set")
		}
		_, exists, err := a.Creds.Get(ctx, *conn.CredentialRef)
		if err != nil {
			return stage(name, "failed", "credential_store_unavailable", "credential store unavailable")
		}
		if !exists {
			return stage(name, "failed", "credential_missing", "the stored credential could not be found")
		}
		return stage(name, "passed", "", "credential is present in the store")
	default:
		if conn.Provider == "gitlab" || conn.Provider == "jira" {
			return a.adapterStage(ctx, name, conn, done, st)
		}
		return stage(name, "not_implemented", "provider_adapter_unavailable", "Provider adapter is not available yet")
	}
}
func (a *App) runConnectionTest(ctx context.Context, job JobDoc, lease Lease) error {
	if job.OperationID == nil {
		return &JobFailure{Code: "no_operation", Message: "job has no operation"}
	}
	id, ok := job.Payload["connection_id"].(string)
	if !ok || !validID("con", id) {
		return &JobFailure{Code: "bad_payload", Message: "missing connection_id"}
	}
	if err := a.fenced(ctx, lease, func(sc context.Context) error { return a.transitionOperation(sc, job, "running", nil) }); err != nil {
		return err
	}
	conn, err := a.connection(ctx, job.OrgID, id)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return &JobFailure{Code: "connection_archived", Message: "connection no longer exists"}
	}
	if err != nil {
		return err
	}
	stages := []StageResult{}
	st := &adapterState{}
	for _, name := range []string{"network", "tls", "http", "credential", "auth", "identity", "capabilities", "sample_access"} {
		if err = ctx.Err(); err != nil {
			return err
		}
		started := time.Now()
		result := a.runStage(ctx, name, conn, stages, st)
		if err = ctx.Err(); err != nil {
			return err
		}
		result.DurationMS = time.Since(started).Milliseconds()
		if err = a.fenced(ctx, lease, func(sc context.Context) error {
			return a.AppendEvent(sc, job.OrgID, "connection_test.stage", Subject{"connection", id}, result, nil, job.OperationID, true)
		}); err != nil {
			return err
		}
		stages = append(stages, result)
	}
	overall := overallStatus(stages)
	finished := time.Now().UTC()
	revision := int64(0)
	switch v := job.Payload["connection_revision"].(type) {
	case int64:
		revision = v
	case int32:
		revision = int64(v)
	case int:
		revision = int64(v)
	}
	report := bson.M{"connection_id": id, "base_url": conn.BaseURL, "tested_revision": revision, "stages": stages, "overall": overall, "finished_at": timestamp(finished)}
	m, err := toMap(report)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(redact(m), "", "  ")
	if err != nil {
		return err
	}
	blob, err := storeBlob(filepath.Join(a.Config.DataDir, "artifacts"), job.OrgID, bytes.NewReader(data), a.Config.ArtifactMaxBytes)
	if err != nil {
		return err
	}
	return a.fenced(ctx, lease, func(sc context.Context) error {
		art, err := a.recordArtifact(sc, job.OrgID, blob, "application/json", "connection_test_report", Subject{"operation", *job.OperationID})
		if err != nil {
			return err
		}
		summary := TestSummary{OperationID: *job.OperationID, ArtifactID: &art.ID, FinishedAt: finished, Overall: overall, Stages: stages, TestedMaterialHash: conn.MaterialHash()}
		set := bson.M{"status": overall, "last_test": summary}
		eventPayload := bson.M{"overall": overall, "artifact_id": art.ID}
		if identity := st.identitySnapshot(finished); identity != nil {
			set["identity"] = identity
			eventPayload["identity"] = bson.M{"username": identity.Username}
		}
		if caps := st.capabilitySnapshot(finished); caps != nil {
			set["capabilities"] = caps
			eventPayload["capabilities"] = caps.Features
		}
		if _, err = a.DB.Coll("connections").UpdateOne(sc, connFilter(job.OrgID, id), bson.M{"$set": set}); err != nil {
			return err
		}
		if err = a.transitionOperation(sc, job, "succeeded", bson.M{"overall": overall, "artifact_id": art.ID}); err != nil {
			return err
		}
		if err = a.AppendEvent(sc, job.OrgID, "connection.tested", Subject{"connection", id}, eventPayload, nil, job.OperationID, true); err != nil {
			return err
		}
		return a.completeJob(sc, lease)
	})
}
