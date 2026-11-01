// Package policy holds the typed settings registry, override validation and layered resolution. It is pure.
package policy

import (
	"fmt"
	"math"
	"sort"
)

const (
	ProductDefaultsVersion = 5
	SchemaVersion          = 1

	LayerProduct = "product"
	LayerOrg     = "org"
	LayerProject = "project"
)

type Key struct {
	Key                string
	Type               string // enum, int, string, bool
	Enum               []string
	Min, Max           int64
	MaxLen             int
	AllowEmpty         bool // a string key whose empty value is valid (and its default)
	Default            any
	Locked             bool
	ProjectOverridable bool
}

var manualAuto = []string{"manual", "auto"}
var promotionModes = []string{"off", "manual", "auto"}
var riskClasses = []string{"low", "medium", "sensitive"}

// Registry lists every setting (Sprint 2 and 3), sorted by key.
var Registry = sortedRegistry([]Key{
	{Key: "automation.intake", Type: "enum", Enum: manualAuto, Default: "manual", ProjectOverridable: true},
	{Key: "automation.coding_start", Type: "enum", Enum: manualAuto, Default: "manual", ProjectOverridable: true},
	{Key: "automation.plan_approval", Type: "enum", Enum: manualAuto, Default: "manual", ProjectOverridable: true},
	{Key: "automation.publish_draft_mr", Type: "enum", Enum: manualAuto, Default: "manual", ProjectOverridable: true},
	{Key: "automation.publish_comments", Type: "enum", Enum: manualAuto, Default: "manual", ProjectOverridable: true},
	{Key: "automation.jira_write", Type: "enum", Enum: []string{"off", "manual", "auto"}, Default: "off", ProjectOverridable: true},
	{Key: "intake.poll_interval_secs", Type: "int", Min: 30, Max: 3600, Default: int64(120), ProjectOverridable: true},
	{Key: "intake.label", Type: "string", MaxLen: 255, Default: "ai-ready", ProjectOverridable: true},
	{Key: "intake.require_assigned_to_me", Type: "bool", Default: true, ProjectOverridable: true},
	{Key: "review.max_fix_rounds", Type: "int", Min: 0, Max: 5, Default: int64(2), ProjectOverridable: true},
	{Key: "review.require_different_model", Type: "bool", Default: true, Locked: true},
	{Key: "ci.require_pipeline", Type: "bool", Default: true, ProjectOverridable: true},
	{Key: "concurrency.coding_runs", Type: "int", Min: 1, Max: 4, Default: int64(1)},
	{Key: "orchestrator.host", Type: "enum", Enum: []string{"codex", "claude-code"}, Default: "codex"},
	{Key: "context.packet_bytes", Type: "int", Min: 4096, Max: 65536, Default: int64(16384), ProjectOverridable: true},
	// Sprint 3 (product defaults version 2).
	{Key: "intake.require_open_sprint", Type: "bool", Default: true, ProjectOverridable: true},
	{Key: "intake.reconcile_every_polls", Type: "int", Min: 1, Max: 100, Default: int64(10), ProjectOverridable: true},
	{Key: "intake.max_issues_per_poll", Type: "int", Min: 10, Max: 1000, Default: int64(200), ProjectOverridable: true},
	{Key: "decision.jev", Type: "enum", Enum: []string{"off", "auto"}, Default: "auto", ProjectOverridable: true},
	{Key: "risk.area.auth", Type: "enum", Enum: riskClasses, Default: "sensitive", ProjectOverridable: true},
	{Key: "risk.area.payment", Type: "enum", Enum: riskClasses, Default: "sensitive", ProjectOverridable: true},
	{Key: "risk.area.migration", Type: "enum", Enum: riskClasses, Default: "sensitive", ProjectOverridable: true},
	{Key: "risk.area.data_deletion", Type: "enum", Enum: riskClasses, Default: "sensitive", ProjectOverridable: true},
	{Key: "risk.area.ci_config", Type: "enum", Enum: riskClasses, Default: "sensitive", ProjectOverridable: true},
	{Key: "risk.area.cross_service", Type: "enum", Enum: riskClasses, Default: "sensitive", ProjectOverridable: true},
	{Key: "workspace.min_free_mb", Type: "int", Min: 512, Max: 102400, Default: int64(2048)},
	{Key: "workspace.max_retained", Type: "int", Min: 1, Max: 100, Default: int64(10)},
	// Sprint 4 (product defaults version 3).
	{Key: "ci.poll_interval_secs", Type: "int", Min: 15, Max: 900, Default: int64(60), ProjectOverridable: true},
	{Key: "ci.no_pipeline_grace_secs", Type: "int", Min: 0, Max: 3600, Default: int64(300), ProjectOverridable: true},
	{Key: "ci.max_wait_secs", Type: "int", Min: 600, Max: 172800, Default: int64(86400), ProjectOverridable: true},
	{Key: "verification.allow_baseline_failures", Type: "bool", Default: false, ProjectOverridable: true},
	{Key: "verification.sandbox", Type: "enum", Enum: []string{"enforce", "best_effort"}, Default: "enforce", ProjectOverridable: true},
	{Key: "verification.max_output_bytes", Type: "int", Min: 1048576, Max: 16777216, Default: int64(4194304)},
	{Key: "roles.max_wall_secs", Type: "int", Min: 600, Max: 86400, Default: int64(7200), ProjectOverridable: true},
	{Key: "roles.max_dispatches_per_run", Type: "int", Min: 4, Max: 50, Default: int64(12), ProjectOverridable: true},
	{Key: "roles.poll_interval_secs", Type: "int", Min: 2, Max: 60, Default: int64(5)},
	{Key: "host.orchestrator_model", Type: "string", MaxLen: 128, AllowEmpty: true, Default: ""},
	{Key: "host.orchestrator_effort", Type: "enum", Enum: []string{"low", "medium", "high"}, Default: "low"},
	{Key: "host.idle_stop_secs", Type: "int", Min: 60, Max: 7200, Default: int64(600)},
	{Key: "host.turn_timeout_secs", Type: "int", Min: 30, Max: 600, Default: int64(120)},
	{Key: "publish.commit_author_name", Type: "string", MaxLen: 100, Default: "Orqen"},
	{Key: "publish.commit_author_email", Type: "string", MaxLen: 254, Default: "orqen@localhost"},
	{Key: "review.on_foreign_push", Type: "enum", Enum: []string{"reverify", "inbox"}, Default: "reverify", ProjectOverridable: true},
	// Sprint 5 (product defaults version 4).
	{Key: "knowledge.promotion.business", Type: "enum", Enum: promotionModes, Default: "manual", ProjectOverridable: true},
	{Key: "knowledge.promotion.project", Type: "enum", Enum: promotionModes, Default: "manual", ProjectOverridable: true},
	{Key: "knowledge.promotion.sprint", Type: "enum", Enum: promotionModes, Default: "manual", ProjectOverridable: true},
	{Key: "knowledge.promotion.task", Type: "enum", Enum: promotionModes, Default: "manual", ProjectOverridable: true},
	{Key: "knowledge.promotion.experience", Type: "enum", Enum: promotionModes, Default: "manual", ProjectOverridable: true},
	{Key: "knowledge.learn_on_merge", Type: "bool", Default: true, ProjectOverridable: true},
	{Key: "knowledge.author", Type: "enum", Enum: []string{"deterministic", "roles"}, Default: "deterministic", ProjectOverridable: true},
	{Key: "knowledge.retrieval.strategy", Type: "enum", Enum: []string{"scoped_text", "pinned_only", "off"}, Default: "scoped_text", ProjectOverridable: true},
	{Key: "knowledge.retrieval.top_k", Type: "int", Min: 1, Max: 50, Default: int64(8), ProjectOverridable: true},
	{Key: "knowledge.retrieval.max_source_bytes", Type: "int", Min: 256, Max: 8192, Default: int64(2048), ProjectOverridable: true},
	{Key: "knowledge.retrieval.max_age_days", Type: "int", Min: 0, Max: 3650, Default: int64(0), ProjectOverridable: true},
	{Key: "knowledge.retrieval.include_stale", Type: "bool", Default: false, ProjectOverridable: true},
	{Key: "knowledge.retrieval.cache_ttl_secs", Type: "int", Min: 0, Max: 86400, Default: int64(600)},
	{Key: "knowledge.compaction.threshold", Type: "int", Min: 3, Max: 50, Default: int64(5), ProjectOverridable: true},
	{Key: "knowledge.wiki.sandbox", Type: "enum", Enum: []string{"enforce", "best_effort"}, Default: "enforce"},
	{Key: "docs.sync_interval_secs", Type: "int", Min: 0, Max: 86400, Default: int64(3600), ProjectOverridable: true},
	{Key: "docs.max_page_bytes", Type: "int", Min: 65536, Max: 4194304, Default: int64(1048576)},
	{Key: "docs.linked_pages_per_run", Type: "int", Min: 0, Max: 20, Default: int64(5), ProjectOverridable: true},
	{Key: "graph.inferred_retention_days", Type: "int", Min: 1, Max: 365, Default: int64(30)},
	{Key: "retention.derived_cache_days", Type: "int", Min: 1, Max: 365, Default: int64(30)},
	{Key: "retention.outbox_days", Type: "int", Min: 1, Max: 365, Default: int64(30)},
	// Sprint 6 (product defaults version 5).
	{Key: "automation.security_scan", Type: "enum", Enum: []string{"off", "manual", "auto"}, Default: "off", ProjectOverridable: true},
	{Key: "security.gate", Type: "enum", Enum: []string{"off", "advisory", "required"}, Default: "off", ProjectOverridable: true},
	{Key: "security.blocking_severity", Type: "enum", Enum: []string{"critical", "high", "medium", "low"}, Default: "high", ProjectOverridable: true},
	{Key: "security.model", Type: "string", MaxLen: 200, AllowEmpty: true, Default: ""},
	{Key: "security.budget_mode", Type: "enum", Enum: []string{"unset", "capped", "unlimited"}, Default: "unset"},
	{Key: "security.budget_usd", Type: "int", Min: 0, Max: 100000, Default: int64(0)},
	{Key: "security.max_turns", Type: "int", Min: 1, Max: 1000, Default: int64(50), ProjectOverridable: true},
	{Key: "security.timeout_secs", Type: "int", Min: 300, Max: 14400, Default: int64(3600)},
	{Key: "codegraph.auto_index", Type: "enum", Enum: []string{"off", "on_run"}, Default: "off", ProjectOverridable: true},
	{Key: "codegraph.impact_max_bytes", Type: "int", Min: 512, Max: 8192, Default: int64(2048), ProjectOverridable: true},
	{Key: "codegraph.keep_indexes_per_repo", Type: "int", Min: 1, Max: 20, Default: int64(3)},
	{Key: "skills.max_bytes_per_role", Type: "int", Min: 1024, Max: 16384, Default: int64(6144), ProjectOverridable: true},
	{Key: "extensions.sandbox", Type: "enum", Enum: []string{"enforce", "best_effort"}, Default: "enforce"},
})

func sortedRegistry(keys []Key) []Key {
	sort.Slice(keys, func(i, j int) bool { return keys[i].Key < keys[j].Key })
	return keys
}
func Lookup(key string) (Key, bool) {
	for _, k := range Registry {
		if k.Key == key {
			return k, true
		}
	}
	return Key{}, false
}

type FieldError struct{ Field, Message string }

// ValidateOverrides checks keys and values for a layer (LayerOrg or LayerProject) and returns the normalized map:
// integers become int64. Fields are reported as overrides.<key>.
func ValidateOverrides(layer string, in map[string]any) (map[string]any, []FieldError) {
	out := make(map[string]any, len(in))
	var es []FieldError
	names := make([]string, 0, len(in))
	for k := range in {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		field := "overrides." + name
		key, ok := Lookup(name)
		switch {
		case !ok:
			es = append(es, FieldError{field, "unknown setting"})
			continue
		case key.Locked:
			es = append(es, FieldError{field, "locked"})
			continue
		case layer == LayerProject && !key.ProjectOverridable:
			es = append(es, FieldError{field, "org only"})
			continue
		}
		v, msg := normalize(key, in[name])
		if msg != "" {
			es = append(es, FieldError{field, msg})
			continue
		}
		out[name] = v
	}
	return out, es
}
func normalize(k Key, v any) (any, string) {
	switch k.Type {
	case "enum":
		s, ok := v.(string)
		for _, e := range k.Enum {
			if ok && s == e {
				return s, ""
			}
		}
		return nil, fmt.Sprintf("must be one of %v", k.Enum)
	case "bool":
		b, ok := v.(bool)
		if !ok {
			return nil, "must be true or false"
		}
		return b, ""
	case "string":
		s, ok := v.(string)
		if min := map[bool]int{true: 0, false: 1}[k.AllowEmpty]; !ok || len(s) < min || len(s) > k.MaxLen {
			return nil, fmt.Sprintf("must be %d to %d characters", min, k.MaxLen)
		}
		return s, ""
	default:
		var n int64
		switch x := v.(type) {
		case int64:
			n = x
		case int32:
			n = int64(x)
		case int:
			n = int64(x)
		case float64:
			if x != math.Trunc(x) || math.Abs(x) > 1e15 {
				return nil, "must be a whole number"
			}
			n = int64(x)
		default:
			return nil, "must be a whole number"
		}
		if n < k.Min || n > k.Max {
			return nil, fmt.Sprintf("must be between %d and %d", k.Min, k.Max)
		}
		return n, ""
	}
}
