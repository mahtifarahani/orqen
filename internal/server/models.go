package server

import (
	"go.mongodb.org/mongo-driver/v2/bson"
	"time"
)

type OrganizationDoc struct {
	ID         string     `bson:"_id" json:"_id"`
	Slug       string     `bson:"slug" json:"slug"`
	Name       string     `bson:"name" json:"name"`
	Locale     string     `bson:"locale" json:"locale"`
	Timezone   string     `bson:"timezone" json:"timezone"`
	Revision   int64      `bson:"revision" json:"revision"`
	CreatedAt  time.Time  `bson:"created_at" json:"created_at"`
	UpdatedAt  time.Time  `bson:"updated_at" json:"updated_at"`
	ArchivedAt *time.Time `bson:"archived_at" json:"archived_at"`
}

type StageResult struct {
	Stage      string  `bson:"stage" json:"stage"`
	Status     string  `bson:"status" json:"status"`
	Code       *string `bson:"code" json:"code"`
	Detail     string  `bson:"detail" json:"detail"`
	DurationMS int64   `bson:"duration_ms" json:"duration_ms"`
}

type TestSummary struct {
	OperationID        string        `bson:"operation_id" json:"operation_id"`
	ArtifactID         *string       `bson:"artifact_id" json:"artifact_id"`
	FinishedAt         time.Time     `bson:"finished_at" json:"finished_at"`
	Overall            string        `bson:"overall" json:"overall"`
	Stages             []StageResult `bson:"stages" json:"stages"`
	TestedMaterialHash string        `bson:"tested_material_hash" json:"tested_material_hash"`
}

type ProviderIdentity struct {
	ProviderUserID string    `bson:"provider_user_id" json:"provider_user_id"`
	Username       string    `bson:"username" json:"username"`
	DisplayName    *string   `bson:"display_name" json:"display_name"`
	VerifiedAt     time.Time `bson:"verified_at" json:"verified_at"`
}
type CapabilitySnapshot struct {
	ServerVersion      *string           `bson:"server_version" json:"server_version"`
	DeploymentDetected *string           `bson:"deployment_detected" json:"deployment_detected"`
	TokenScopes        []string          `bson:"token_scopes" json:"token_scopes"`
	Features           map[string]string `bson:"features" json:"features"`
	CheckedAt          time.Time         `bson:"checked_at" json:"checked_at"`
}

type ConnectionDoc struct {
	ID                  string              `bson:"_id" json:"_id"`
	OrgID               string              `bson:"org_id" json:"org_id"`
	Name                string              `bson:"name" json:"name"`
	NameKey             string              `bson:"name_key" json:"name_key"`
	Provider            string              `bson:"provider" json:"provider"`
	Deployment          string              `bson:"deployment" json:"deployment"`
	BaseURL             string              `bson:"base_url" json:"base_url"`
	AuthMethod          string              `bson:"auth_method" json:"auth_method"`
	CredentialRef       *string             `bson:"credential_ref" json:"credential_ref"`
	CredentialState     string              `bson:"credential_state" json:"credential_state"`
	CredentialMissing   bool                `bson:"credential_missing" json:"credential_missing"`
	CredentialUpdatedAt *time.Time          `bson:"credential_updated_at" json:"credential_updated_at"`
	ProxyURL            *string             `bson:"proxy_url" json:"proxy_url"`
	CustomCaPEM         *string             `bson:"custom_ca_pem" json:"custom_ca_pem"`
	TimeoutMS           int                 `bson:"timeout_ms" json:"timeout_ms"`
	MaxConcurrency      int                 `bson:"max_concurrency" json:"max_concurrency"`
	AllowedHosts        []string            `bson:"allowed_hosts" json:"allowed_hosts"`
	Status              string              `bson:"status" json:"status"`
	LastTest            *TestSummary        `bson:"last_test" json:"last_test"`
	AuthUsername        *string             `bson:"auth_username" json:"auth_username"`
	Identity            *ProviderIdentity   `bson:"identity" json:"identity"`
	Capabilities        *CapabilitySnapshot `bson:"capabilities" json:"capabilities"`
	CatalogSummary      *CatalogSummary     `bson:"catalog_summary" json:"catalog_summary"`
	Active              bool                `bson:"active" json:"active"`
	Revision            int64               `bson:"revision" json:"revision"`
	CreatedAt           time.Time           `bson:"created_at" json:"created_at"`
	UpdatedAt           time.Time           `bson:"updated_at" json:"updated_at"`
	ArchivedAt          *time.Time          `bson:"archived_at" json:"archived_at"`
}

type CatalogSummary struct {
	RefreshedAt  time.Time `bson:"refreshed_at" json:"refreshed_at"`
	ProjectCount int64     `bson:"project_count" json:"project_count"`
	OperationID  string    `bson:"operation_id" json:"operation_id"`
}

type OperationDoc struct {
	ID        string    `bson:"_id" json:"_id"`
	OrgID     string    `bson:"org_id" json:"org_id"`
	Kind      string    `bson:"kind" json:"kind"`
	Subject   Subject   `bson:"subject" json:"subject"`
	Status    string    `bson:"status" json:"status"`
	JobID     *string   `bson:"job_id" json:"job_id"`
	Result    *bson.M   `bson:"result" json:"result"`
	Problem   *bson.M   `bson:"problem" json:"problem"`
	CreatedAt time.Time `bson:"created_at" json:"created_at"`
	UpdatedAt time.Time `bson:"updated_at" json:"updated_at"`
	Revision  int64     `bson:"revision" json:"revision"`
}

type ArtifactDoc struct {
	ID        string    `bson:"_id" json:"_id"`
	OrgID     string    `bson:"org_id" json:"org_id"`
	Sha256    string    `bson:"sha256" json:"sha256"`
	Size      int64     `bson:"size" json:"size"`
	MediaType string    `bson:"media_type" json:"media_type"`
	Kind      string    `bson:"kind" json:"kind"`
	Source    Subject   `bson:"source" json:"source"`
	CreatedAt time.Time `bson:"created_at" json:"created_at"`
}

type Subject struct {
	Kind string `bson:"kind" json:"kind"`
	ID   string `bson:"id" json:"id"`
}

type EventDoc struct {
	ID          bson.ObjectID `bson:"_id" json:"_id"`
	Seq         int64         `bson:"seq" json:"seq"`
	OrgID       *string       `bson:"org_id" json:"org_id"`
	Type        string        `bson:"type" json:"type"`
	Actor       string        `bson:"actor" json:"actor"`
	Subject     Subject       `bson:"subject" json:"subject"`
	Revision    *int64        `bson:"revision" json:"revision"`
	OperationID *string       `bson:"operation_id" json:"operation_id"`
	Payload     bson.M        `bson:"payload" json:"payload"`
	CreatedAt   time.Time     `bson:"created_at" json:"created_at"`
}

type JobDoc struct {
	ID             string     `bson:"_id" json:"_id"`
	OrgID          string     `bson:"org_id" json:"org_id"`
	Kind           string     `bson:"kind" json:"kind"`
	DedupeKey      string     `bson:"dedupe_key" json:"dedupe_key"`
	Payload        bson.M     `bson:"payload" json:"payload"`
	Rerun          bool       `bson:"rerun,omitempty" json:"rerun,omitempty"`
	State          string     `bson:"state" json:"state"`
	Active         bool       `bson:"active" json:"active"`
	Attempts       int        `bson:"attempts" json:"attempts"`
	MaxAttempts    int        `bson:"max_attempts" json:"max_attempts"`
	NextAt         time.Time  `bson:"next_at" json:"next_at"`
	LeaseOwner     *string    `bson:"lease_owner" json:"lease_owner"`
	LeaseExpiresAt *time.Time `bson:"lease_expires_at" json:"lease_expires_at"`
	Fence          int64      `bson:"fence" json:"fence"`
	OperationID    *string    `bson:"operation_id" json:"operation_id"`
	LastError      *bson.M    `bson:"last_error" json:"last_error"`
	CreatedAt      time.Time  `bson:"created_at" json:"created_at"`
	UpdatedAt      time.Time  `bson:"updated_at" json:"updated_at"`
}

type SessionDoc struct {
	ID         string    `bson:"_id" json:"_id"`
	TokenHash  string    `bson:"token_hash" json:"token_hash"`
	CsrfToken  string    `bson:"csrf_token" json:"csrf_token"`
	CreatedAt  time.Time `bson:"created_at" json:"created_at"`
	LastSeenAt time.Time `bson:"last_seen_at" json:"last_seen_at"`
	ExpiresAt  time.Time `bson:"expires_at" json:"expires_at"`
}

type OneTimeTokenDoc struct {
	ID        string     `bson:"_id" json:"_id"`
	TokenHash string     `bson:"token_hash" json:"token_hash"`
	Purpose   string     `bson:"purpose" json:"purpose"`
	ExpiresAt time.Time  `bson:"expires_at" json:"expires_at"`
	UsedAt    *time.Time `bson:"used_at" json:"used_at"`
}

type IdemRecord struct {
	ID              string    `bson:"_id" json:"_id"`
	Scope           string    `bson:"scope" json:"scope"`
	Route           string    `bson:"route" json:"route"`
	Key             string    `bson:"key" json:"key"`
	RequestHash     string    `bson:"request_hash" json:"request_hash"`
	State           string    `bson:"state" json:"state"`
	ClaimToken      string    `bson:"claim_token" json:"claim_token"`
	LockedUntil     time.Time `bson:"locked_until" json:"locked_until"`
	ResponseStatus  *int      `bson:"response_status" json:"response_status"`
	ResponseBody    *bson.M   `bson:"response_body" json:"response_body"`
	ResponseHeaders *bson.M   `bson:"response_headers" json:"response_headers"`
	CreatedAt       time.Time `bson:"created_at" json:"created_at"`
	ExpiresAt       time.Time `bson:"expires_at" json:"expires_at"`
}

func timestamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }
func optionalTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return timestamp(*t)
}
func (d OrganizationDoc) DTO() bson.M {
	return bson.M{"id": d.ID, "slug": d.Slug, "name": d.Name, "locale": d.Locale, "timezone": d.Timezone, "revision": d.Revision, "created_at": timestamp(d.CreatedAt), "updated_at": timestamp(d.UpdatedAt)}
}

// MaterialHash covers every connection field that changes what a test would prove.
func (d ConnectionDoc) MaterialHash() string {
	return sha256Hex(canonicalJSON(bson.M{"base_url": d.BaseURL, "auth_method": d.AuthMethod, "auth_username": d.AuthUsername, "proxy_url": d.ProxyURL, "custom_ca_pem": d.CustomCaPEM, "allowed_hosts": d.AllowedHosts, "credential_ref": d.CredentialRef}))
}

var adapterStageReasons = []struct{ stage, reason string }{{"auth", "auth_failed"}, {"identity", "identity_unverified"}, {"capabilities", "capability_missing"}, {"sample_access", "sample_access_failed"}}

func (d ConnectionDoc) readinessReasons() []string {
	reasons := []string{}
	if d.CredentialMissing {
		reasons = append(reasons, "credential_missing") // a restore found no value for the stored reference (X34)
	} else if d.CredentialState == "none" {
		reasons = append(reasons, "no_api_credential")
	}
	if d.LastTest == nil {
		reasons = append(reasons, "not_tested")
	} else if d.LastTest.TestedMaterialHash != d.MaterialHash() {
		reasons = append(reasons, "test_stale")
	} else if d.Provider == "gitlab" || d.Provider == "jira" {
		for _, r := range adapterStageReasons {
			passed := false
			for _, s := range d.LastTest.Stages {
				if s.Stage == r.stage && s.Status == "passed" {
					passed = true
				}
			}
			if !passed {
				reasons = append(reasons, r.reason)
			}
		}
	}
	if d.Provider != "gitlab" && d.Provider != "jira" {
		reasons = append(reasons, "provider_adapter_unavailable")
	}
	return reasons
}
func (d ConnectionDoc) DTO() bson.M {
	reasons := d.readinessReasons()
	var last any
	if t := d.LastTest; t != nil {
		last = bson.M{"operation_id": t.OperationID, "artifact_id": t.ArtifactID, "finished_at": timestamp(t.FinishedAt), "overall": t.Overall, "stages": t.Stages}
	}
	var identity any
	if i := d.Identity; i != nil {
		identity = bson.M{"provider_user_id": i.ProviderUserID, "username": i.Username, "display_name": i.DisplayName, "verified_at": timestamp(i.VerifiedAt)}
	}
	var caps any
	if c := d.Capabilities; c != nil {
		features := c.Features
		if features == nil {
			features = map[string]string{}
		}
		caps = bson.M{"server_version": c.ServerVersion, "deployment_detected": c.DeploymentDetected, "token_scopes": c.TokenScopes, "features": features, "checked_at": timestamp(c.CheckedAt)}
	}
	hosts := d.AllowedHosts
	if hosts == nil {
		hosts = []string{}
	}
	return bson.M{"id": d.ID, "org_id": d.OrgID, "name": d.Name, "provider": d.Provider, "deployment": d.Deployment, "base_url": d.BaseURL, "auth_method": d.AuthMethod, "auth_username": d.AuthUsername, "proxy_url": d.ProxyURL, "custom_ca_pem": d.CustomCaPEM, "timeout_ms": d.TimeoutMS, "max_concurrency": d.MaxConcurrency, "allowed_hosts": hosts, "status": d.Status, "last_test": last, "identity": identity, "capabilities": caps, "credential": bson.M{"state": d.CredentialState, "updated_at": optionalTime(d.CredentialUpdatedAt)}, "readiness": bson.M{"unattended_ready": len(reasons) == 0, "reasons": reasons}, "revision": d.Revision, "created_at": timestamp(d.CreatedAt), "updated_at": timestamp(d.UpdatedAt)}
}
func (d OperationDoc) DTO() bson.M {
	return bson.M{"id": d.ID, "kind": d.Kind, "subject": d.Subject, "status": d.Status, "result": d.Result, "problem": d.Problem, "created_at": timestamp(d.CreatedAt), "updated_at": timestamp(d.UpdatedAt), "revision": d.Revision}
}
func (d ArtifactDoc) DTO() bson.M {
	return bson.M{"id": d.ID, "sha256": d.Sha256, "size": d.Size, "media_type": d.MediaType, "kind": d.Kind, "source": d.Source, "created_at": timestamp(d.CreatedAt)}
}
func (d EventDoc) DTO() bson.M {
	return bson.M{"seq": d.Seq, "type": d.Type, "actor": d.Actor, "subject": d.Subject, "revision": d.Revision, "operation_id": d.OperationID, "payload": d.Payload, "created_at": timestamp(d.CreatedAt)}
}
func ptr[T any](v T) *T { return &v }
