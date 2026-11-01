package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"log/slog"
	"net/http"
	"orqen/internal/providers"
	"strconv"
	"time"
)

type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}
type AppError struct {
	Code       string
	Status     int
	Detail     string
	Errors     []FieldError
	Current    any
	RetryAfter time.Duration
}

func (e *AppError) Error() string { return e.Detail }
func problem(code string) *AppError {
	status := 500
	detail := code
	switch code {
	case "not_found":
		status = 404
		detail = "not found"
	case "revision_conflict":
		status = 409
		detail = "revision conflict"
	case "precondition_required":
		status = 428
		detail = "precondition required"
	case "idempotency_key_reused":
		status = 422
		detail = "idempotency key reused with a different request"
	case "idempotency_in_progress":
		status = 409
		detail = "a request with this idempotency key is in progress"
	case "unauthenticated":
		status = 401
		detail = "authentication required"
	case "invalid_token":
		status = 401
		detail = "invalid or expired token"
	case "csrf_failed":
		status = 403
		detail = "csrf check failed"
	case "origin_rejected":
		status = 403
		detail = "origin rejected"
	case "host_rejected":
		status = 421
		detail = "host rejected"
	case "already_bootstrapped":
		status = 409
		detail = "already bootstrapped"
	case "payload_too_large":
		status = 413
		detail = "payload too large"
	case "credential_store_unavailable":
		status = 503
		detail = "credential store unavailable"
	case "database_unavailable":
		status = 503
		detail = "the database is unavailable"
	case "project_not_ready":
		status = 409
		detail = "the project is not ready for automation"
	case "automation_enabled":
		status = 409
		detail = "automation is enabled for this project"
	case "profile_not_validated":
		status = 409
		detail = "the draft has no current valid validation"
	case "profile_in_use":
		status = 409
		detail = "the profile is bound to an active project"
	case "wizard_step_incomplete":
		status = 409
		detail = "the setup step is not complete"
	case "version_not_published":
		status = 422
		detail = "the profile version is not published"
	case "repo_already_mapped":
		status = 422
		detail = "the repository is already mapped to a project"
	case "provider_mismatch":
		status = 422
		detail = "provider mismatch"
	case "credential_required":
		status = 422
		detail = "the connection has no usable credential"
	case "capability_unsupported":
		status = 422
		detail = "the provider does not support this capability"
	case "catherd_unavailable":
		status = 503
		detail = "catherd is not available"
	case "catherd_version_unsupported":
		status = 503
		detail = "this catherd version is not supported"
	case "run_state_conflict":
		status = 409
		detail = "the run is not in a state that allows this action"
	case "run_not_startable":
		status = 409
		detail = "the run cannot start yet"
	case "inbox_item_stale":
		status = 409
		detail = "this question was replaced or already answered"
	case "automation_paused":
		status = 409
		detail = "automation is paused for this organization"
	case "answer_invalid":
		status = 422
		detail = "the answer is not valid for this question"
	case "issue_inaccessible":
		status = 422
		detail = "the issue could not be read"
	case "jira_mapping_missing":
		status = 422
		detail = "the project has no Jira mapping"
	case "workspace_dirty":
		status = 409
		detail = "the workspace has uncommitted changes"
	case "catherd_failed":
		status = 502
		detail = "catherd failed"
	case "external_writes_not_live":
		status = 409
		detail = "external writes are not live for this organization"
	case "write_target_not_allowlisted":
		status = 422
		detail = "the target project is not on the write allowlist"
	case "env_guard_refused":
		status = 409
		detail = "this build may not write to that host"
	case "intent_changed":
		status = 409
		detail = "the operation changed since it was shown"
	case "external_writes_paused":
		status = 409
		detail = "external writes are paused"
	case "write_credential_missing":
		status = 422
		detail = "the connection has no write credential"
	case "write_credential_same_as_read":
		status = 422
		detail = "the write credential must differ from the read credential"
	case "confirm_phrase_mismatch":
		status = 422
		detail = "the confirmation phrase does not match"
	case "allowlist_invalid":
		status = 422
		detail = "the allowlist is not valid"
	case "finding_not_waivable":
		status = 409
		detail = "the finding is not an open blocking finding"
	case "host_not_probed":
		status = 409
		detail = "the Codex host has not been probed"
	case "knowledge_key_taken":
		status, detail = 409, "a record with this scope and key already exists"
	case "knowledge_draft_exists":
		status, detail = 409, "the record already has an open draft version"
	case "knowledge_version_not_draft":
		status, detail = 409, "the version is not a draft"
	case "knowledge_conflict_open":
		status, detail = 409, "the version conflicts with verified knowledge; resolve the conflict first"
	case "conflict_not_open":
		status, detail = 409, "the conflict is not open"
	case "knowledge_not_verified":
		status, detail = 409, "the record has no verified version"
	case "knowledge_pin_type_invalid":
		status, detail = 422, "only business and project knowledge can be critical"
	case "rollback_target_invalid":
		status, detail = 422, "the rollback target is not an earlier verified version of this record"
	case "extension_not_registered", "extension_not_detected", "extension_not_tested", "extension_not_enabled", "extension_state_invalid", "extension_detect_only", "extension_binary_changed":
		status, detail = 409, "the extension is not in a state that allows this"
	case "license_acceptance_required":
		status, detail = 409, "the license of this extension must be accepted first"
	case "license_not_required":
		status, detail = 409, "this extension has no license gate"
	case "license_revoked":
		status, detail = 409, "the license of this extension was revoked"
	case "credential_missing":
		status, detail = 409, "a required credential of the extension is not set"
	case "license_notice_mismatch", "license_input_invalid", "extension_path_invalid", "extension_config_invalid", "credential_field_unknown":
		status, detail = 422, "the request is not valid for this extension"
	case "skill_draft_exists", "skill_slug_builtin", "skill_version_not_draft", "skill_bindings_not_empty":
		status, detail = 409, "the skill request conflicts with the current skills"
	case "skill_invalid", "skill_too_large", "skill_flags_unacknowledged", "skill_lint_blocked", "skill_binding_invalid":
		status, detail = 422, "the skill is not valid for this request"
	case "extension_not_usable", "project_not_bound", "index_not_ready":
		status, detail = 409, "the code index request is not possible in the current state"
	case "scan_head_unreviewed", "docker_unavailable":
		status, detail = 409, "the scan cannot start in the current state"
	case "security_model_unset", "security_budget_unset", "scan_target_unsupported":
		status, detail = 422, "the security scan settings are not complete"
	case "kas_terminal", "kas_not_failed", "kas_active":
		status, detail = 409, "the knowledge author session is not in a state that allows this"
	case "backup_dir_invalid", "confirm_required":
		status, detail = 422, "the backup request is not valid"
	case "support_bundle_secret_detected":
		status, detail = 500, "the support bundle contained something that looks like a secret and was withheld"
	case "restore_integrity_failed":
		status, detail = 409, "the backup failed its integrity check"
	case "connection_kind_unsupported":
		status = 422
		detail = "this connection kind has no write credential"
	}
	return &AppError{Code: code, Status: status, Detail: detail}
}

// providerProblem maps a provider failure to an HTTP problem. Other errors pass through.
func providerProblem(err error) error {
	pe, ok := providers.AsError(err)
	if !ok {
		return err
	}
	switch {
	case pe.Code == "credential_missing":
		return &AppError{Code: "credential_required", Status: 422, Detail: pe.Message}
	case pe.Kind == providers.KindUnsupported:
		return &AppError{Code: "capability_unsupported", Status: 422, Detail: pe.Message}
	case pe.Kind == providers.KindConfig:
		return &AppError{Code: "provider_invalid_configuration", Status: 422, Detail: pe.Message}
	}
	return &AppError{Code: "provider_" + string(pe.Kind), Status: 502, Detail: pe.Message, RetryAfter: pe.RetryAfter}
}
func validation(field, message string) *AppError {
	return &AppError{Code: "validation_failed", Status: 422, Detail: "validation failed", Errors: []FieldError{{field, message}}}
}
func validationErrors(es []FieldError) error {
	if len(es) == 0 {
		return nil
	}
	return &AppError{Code: "validation_failed", Status: 422, Detail: "validation failed", Errors: es}
}
func asAppError(err error) *AppError {
	var e *AppError
	if errors.As(err, &e) {
		return e
	}
	if errors.Is(err, mongo.ErrNoDocuments) {
		return problem("not_found")
	}
	if mongo.IsNetworkError(err) || mongo.IsTimeout(err) {
		return problem("database_unavailable")
	}
	return problem("internal")
}
func errorBody(err error, requestID string) map[string]any {
	e := asAppError(err)
	title := map[int]string{422: "Validation failed", 404: "Not found", 409: "Conflict", 428: "Precondition required", 401: "Unauthenticated", 403: "Forbidden", 421: "Misdirected request", 413: "Payload too large", 502: "Provider error", 503: "Service unavailable"}[e.Status]
	if title == "" {
		title = "Internal error"
	}
	detail := e.Detail
	if e.Status == 500 {
		detail = fmt.Sprintf("internal error, see logs request_id=%s", requestID)
	}
	b := map[string]any{"type": "https://orqen.dev/problems/" + e.Code, "title": title, "status": e.Status, "code": e.Code, "detail": detail, "request_id": requestID}
	if len(e.Errors) > 0 {
		b["errors"] = e.Errors
	}
	if e.Current != nil {
		b["current"] = e.Current
	}
	return b
}
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	e := asAppError(err)
	if e.Status >= 500 {
		slog.Error("request failed", "request_id", requestID(r), "error", err)
	}
	if e.Code == "idempotency_in_progress" {
		w.Header().Set("Retry-After", "1")
	}
	if e.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(e.RetryAfter.Seconds())+1))
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(errorBody(err, requestID(r)))
}
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
