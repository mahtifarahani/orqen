package server

import (
	"context"
	"errors"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readconcern"
	"go.mongodb.org/mongo-driver/v2/mongo/writeconcern"
	"sync/atomic"
	"time"
)

type DB struct {
	Client   *mongo.Client
	Database *mongo.Database
	// lastOp is the newest operation time this process has committed or read through Txn, packed as T<<32|I. A new
	// transaction starts causally after it: a snapshot transaction opened right after another commit otherwise reads
	// the previous majority snapshot and answers If-Match with a stale revision.
	lastOp atomic.Uint64
}
type SchemaTooNew struct{ Found int }

func (e *SchemaTooNew) Error() string {
	return fmt.Sprintf("database schema version %d is newer than this build supports (%d)", e.Found, SchemaVersion)
}
func OpenDB(ctx context.Context, c Config) (*DB, error) {
	opts := options.Client().ApplyURI(c.MongoURI).SetAppName("orqen")
	if opts.ServerSelectionTimeout == nil {
		opts.SetServerSelectionTimeout(5 * time.Second)
	}
	client, err := mongo.Connect(opts)
	if err != nil {
		return nil, err
	}
	db := &DB{Client: client, Database: client.Database(c.DBName)}
	if _, err = db.ReplicaSet(ctx); err != nil {
		_ = client.Disconnect(ctx)
		return nil, err
	}
	if err = db.Migrate(ctx); err != nil {
		_ = client.Disconnect(ctx)
		return nil, err
	}
	return db, nil
}
func (d *DB) Coll(name string) *mongo.Collection { return d.Database.Collection(name) }
func (d *DB) ReplicaSet(ctx context.Context) (string, error) {
	var hello struct {
		SetName string `bson:"setName"`
	}
	err := d.Database.RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).Decode(&hello)
	if err != nil {
		return "", err
	}
	if hello.SetName == "" {
		return "", errors.New(NotReplicaSet)
	}
	return hello.SetName, nil
}

// Members counts the replica set members the server reports.
func (d *DB) Members(ctx context.Context) (int, error) {
	var hello struct {
		Hosts []string `bson:"hosts"`
	}
	if err := d.Database.RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).Decode(&hello); err != nil {
		return 0, err
	}
	return len(hello.Hosts), nil
}
func (d *DB) Txn(ctx context.Context, f func(context.Context) error) error {
	session, err := d.Client.StartSession()
	if err != nil {
		return err
	}
	defer session.EndSession(ctx)
	if last := d.lastOp.Load(); last != 0 {
		_ = session.AdvanceOperationTime(&bson.Timestamp{T: uint32(last >> 32), I: uint32(last)})
	}
	_, err = session.WithTransaction(ctx, func(sc context.Context) (any, error) { return nil, f(sc) }, options.Transaction().SetReadConcern(readconcern.Snapshot()).SetWriteConcern(writeconcern.Majority()))
	if ts := session.OperationTime(); ts != nil {
		packed := uint64(ts.T)<<32 | uint64(ts.I)
		for {
			cur := d.lastOp.Load()
			if packed <= cur || d.lastOp.CompareAndSwap(cur, packed) {
				break
			}
		}
	}
	return err
}
func (d *DB) Migrate(ctx context.Context) error {
	var meta struct {
		Version int `bson:"version"`
	}
	err := d.Coll("schema_meta").FindOne(ctx, bson.M{"_id": "schema"}).Decode(&meta)
	if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
		return err
	}
	if meta.Version > SchemaVersion {
		return &SchemaTooNew{meta.Version}
	}
	type index struct {
		coll, name string
		keys       bson.D
		unique     bool
		partial    bson.M
		ttl        bool
	}
	key := func(names ...string) bson.D {
		r := bson.D{}
		for _, n := range names {
			r = append(r, bson.E{Key: n, Value: 1})
		}
		return r
	}
	// Schema v2 adds the Sprint 2 collections below; v1 data needs no conversion.
	indexes := []index{
		{"operations", "org_id", key("org_id", "_id"), false, nil, false},
		{"artifacts", "org_sha256", key("org_id", "sha256"), true, nil, false},
		{"jobs", "state_next", key("state", "next_at"), false, nil, false},
		{"jobs", "state_lease", key("state", "lease_expires_at"), false, nil, false},
		{"jobs", "org_dedupe_active", key("org_id", "dedupe_key"), true, bson.M{"active": true}, false},
		{"jobs", "org_id", key("org_id", "_id"), false, nil, false},
		{"connections", "org_name_active", key("org_id", "name_key"), true, bson.M{"active": true}, false},
		{"connections", "org_id", key("org_id", "_id"), false, nil, false},
		{"events", "seq_unique", key("seq"), true, nil, false},
		{"events", "org_seq", key("org_id", "seq"), false, nil, false},
		{"idempotency_records", "scope_route_key", key("scope", "route", "key"), true, nil, false},
		{"idempotency_records", "expires_ttl", key("expires_at"), false, nil, true},
		{"organizations", "slug_unique", key("slug"), true, nil, false},
		{"sessions", "token_hash_unique", key("token_hash"), true, nil, false},
		{"sessions", "expires_ttl", key("expires_at"), false, nil, true},
		{"one_time_tokens", "token_hash_unique", key("token_hash"), true, nil, false},
		{"one_time_tokens", "expires_ttl", key("expires_at"), false, nil, true},
		{"projects", "org_name_active", key("org_id", "name_key"), true, bson.M{"active": true}, false},
		{"projects", "org_primary_repo", key("org_id", "primary_repo_key"), true, bson.M{"active": true, "primary_repo_key": bson.M{"$type": "string"}}, false},
		{"projects", "org_id", key("org_id", "_id"), false, nil, false},
		{"repo_contract_snapshots", "org_project", key("org_id", "project_id", "_id"), false, nil, false},
		{"policy_drafts", "org", key("org_id"), false, nil, false},
		{"policy_versions", "org_scope_number", key("org_id", "kind", "scope_id", "number"), true, nil, false},
		{"policy_versions", "org_id", key("org_id", "_id"), false, nil, false},
		{"model_profiles", "org_slug_active", key("org_id", "slug"), true, bson.M{"active": true}, false},
		{"model_profiles", "org_id", key("org_id", "_id"), false, nil, false},
		{"model_profile_versions", "org_profile_number", key("org_id", "profile_id", "number"), true, nil, false},
		{"model_profile_versions", "org_catherd_name", key("org_id", "catherd_name"), true, nil, false},
		{"model_profile_versions", "one_publishing", key("org_id", "profile_id"), true, bson.M{"state": "publishing"}, false},
		{"model_profile_versions", "org_id", key("org_id", "_id"), false, nil, false},
		{"setup_wizards", "org", key("org_id"), true, nil, false},
		{"system_states", "org", key("org_id"), true, nil, false},
		{"catherd_states", "org", key("org_id"), false, nil, false},
		{"catherd_secrets", "org_kind_active", key("org_id", "kind"), true, bson.M{"active": true}, false},
		{"catalog_projects", "org_con_pid", key("org_id", "connection_id", "provider_project_id"), true, nil, false},
		{"catalog_projects", "org_con_sort", key("org_id", "connection_id", "gone", "sort_key"), false, nil, false},
		// Sprint 3 collections (additive; the schema version stays 2).
		{"tracked_issues", "org_con_issue", key("org_id", "connection_id", "issue_id"), true, nil, false},
		{"tracked_issues", "org_project_filter", key("org_id", "project_id", "in_filter", "_id"), false, nil, false},
		{"tracked_issues", "org_sort", key("org_id", "sort_key"), false, nil, false},
		{"issue_snapshots", "org_con_issue_rev", key("org_id", "connection_id", "issue_id", "revision"), true, nil, false},
		{"issue_snapshots", "org_con_issue_hash", key("org_id", "connection_id", "issue_id", "content_hash"), true, nil, false},
		{"runs", "org_intent", key("org_id", "intent_key"), true, nil, false},
		{"runs", "org_issue_active", key("org_id", "connection_id", "issue_id"), true, bson.M{"active": true}, false},
		{"runs", "org_state", key("org_id", "state", "_id"), false, nil, false},
		{"runs", "org_project", key("org_id", "project_id", "_id"), false, nil, false},
		{"runs", "org_tracked", key("org_id", "tracked_issue_id", "_id"), false, nil, false},
		{"runs", "active_hold", key("active", "hold.code"), false, nil, false},
		{"run_attempts", "org_run_number", key("org_id", "run_id", "number"), true, nil, false},
		{"inbox_items", "org_run_open", key("org_id", "run_id"), true, bson.M{"status": "open"}, false},
		{"inbox_items", "org_status_sort", key("org_id", "status", "sort_key"), false, nil, false},
		{"inbox_items", "org_project_status_sort", key("org_id", "project_id", "status", "sort_key"), false, nil, false},
		{"decisions", "org_run_request", key("org_id", "run_id", "request_key"), true, nil, false},
		{"decisions", "org_created", key("org_id", "created_at"), false, nil, false},
		{"workspaces", "org_run_active", key("org_id", "run_id"), true, bson.M{"active": true}, false},
		{"workspaces", "org_state", key("org_id", "state", "_id"), false, nil, false},
		{"intake_states", "org_project", key("org_id", "project_id"), true, nil, false},
		{"intake_states", "next_poll", key("next_poll_at"), false, nil, false},
		{"automation_states", "org", key("org_id"), true, nil, false},
		{"pinned_rules", "org_scope_project_active", key("org_id", "scope", "project_id", "active", "_id"), false, nil, false},
		{"events", "org_payload_run_seq", key("org_id", "payload.run_id", "seq"), false, nil, false},
		// Sprint 4 collections (additive; the schema version stays 2).
		{"role_executions", "org_run_name_round", key("org_id", "run_id", "name", "round"), true, nil, false},
		{"role_executions", "org_run_created", key("org_id", "run_id", "created_at"), false, nil, false},
		{"role_executions", "state_updated", key("state", "updated_at"), false, nil, false},
		{"host_sessions", "org_run_live", key("org_id", "run_id"), true, bson.M{"live": true}, false},
		{"host_states", "org", key("org_id"), true, nil, false},
		{"review_findings", "org_run_round_key", key("org_id", "run_id", "round", "finding_key"), true, nil, false},
		{"review_findings", "org_run_status", key("org_id", "run_id", "status"), false, nil, false},
		{"external_operations", "org_intent_key", key("org_id", "intent_key"), true, nil, false},
		{"external_operations", "org_run_created", key("org_id", "run_id", "created_at"), false, nil, false},
		{"external_operations", "org_state_updated", key("org_id", "state", "updated_at"), false, nil, false},
		{"merge_requests", "org_run_active", key("org_id", "run_id"), true, bson.M{"active": true}, false},
		{"merge_requests", "org_updated", key("org_id", "updated_at"), false, nil, false},
		{"verification_evidence", "org_run_kind_head_fp", key("org_id", "run_id", "kind", "head_sha", "commands_fp"), true, nil, false},
		{"verification_evidence", "org_run_created", key("org_id", "run_id", "created_at"), false, nil, false},
		{"external_write_settings", "org", key("org_id"), true, nil, false},
		{"publisher_credentials", "org_con_active", key("org_id", "connection_id"), true, bson.M{"active": true}, false},
		{"events", "org_subject_seq", key("org_id", "subject.kind", "subject.id", "seq"), false, nil, false},
		// Sprint 5 collections (additive; the schema version stays 2).
		{coll: "source_versions", name: "org_kind_con_ext_hash", keys: key("org_id", "kind", "connection_id", "external_id", "content_hash"), unique: true},
		{coll: "source_versions", name: "org_kind_con_ext_latest", keys: key("org_id", "kind", "connection_id", "external_id"), unique: true, partial: bson.M{"latest": true}},
		{coll: "source_versions", name: "org_project_kind_fetched", keys: key("org_id", "project_id", "kind", "fetched_at")},
		{coll: "knowledge_records", name: "org_scope_key", keys: key("org_id", "scope_key", "key"), unique: true},
		{coll: "knowledge_records", name: "org_type_status_updated", keys: key("org_id", "type", "status", "updated_at")},
		{coll: "knowledge_records", name: "org_pinned_critical", keys: key("org_id", "pinned", "critical")},
		{coll: "knowledge_versions", name: "org_record_number", keys: key("org_id", "record_id", "number"), unique: true},
		{coll: "knowledge_versions", name: "org_record_verified", keys: key("org_id", "record_id"), unique: true, partial: bson.M{"status": "verified"}},
		{coll: "knowledge_versions", name: "org_record_draft", keys: key("org_id", "record_id"), unique: true, partial: bson.M{"status": "draft"}},
		{coll: "knowledge_versions", name: "org_status_type_scope", keys: key("org_id", "status", "type", "scope_key")},
		{coll: "knowledge_proposals", name: "org_dedupe", keys: key("org_id", "dedupe_key"), unique: true},
		{coll: "knowledge_proposals", name: "org_state_created", keys: key("org_id", "state", "created_at")},
		{coll: "knowledge_conflicts", name: "org_fingerprint_open", keys: key("org_id", "fingerprint"), unique: true, partial: bson.M{"state": "open"}},
		{coll: "knowledge_conflicts", name: "org_state_created", keys: key("org_id", "state", "created_at")},
		{coll: "knowledge_conflicts", name: "org_versions", keys: key("org_id", "version_ids")},
		{coll: "wiki_outbox", name: "org_seq", keys: key("org_id", "seq"), unique: true},
		{coll: "wiki_outbox", name: "org_state_seq", keys: key("org_id", "state", "seq")},
		{coll: "wiki_outbox", name: "purge_ttl", keys: key("purge_at"), ttl: true},
		{coll: "source_sync_states", name: "org", keys: key("org_id")},
		{coll: "wiki_vault_states", name: "org", keys: key("org_id"), unique: true},
		{coll: "knowledge_search_cache", name: "org_key", keys: key("org_id", "key"), unique: true},
		{coll: "knowledge_search_cache", name: "expires_ttl", keys: key("expires_at"), ttl: true},
		{coll: "knowledge_usage", name: "org_run", keys: key("org_id", "run_id")},
		{coll: "knowledge_usage", name: "org_version_created", keys: key("org_id", "version_id", "created_at")},
		{coll: "graph_nodes", name: "org_kind_key", keys: key("org_id", "kind", "natural_key"), unique: true},
		{coll: "graph_edges", name: "org_from_to_rel", keys: key("org_id", "from", "to", "relation"), unique: true},
		{coll: "graph_edges", name: "org_from_status", keys: key("org_id", "from", "status")},
		{coll: "graph_edges", name: "org_to_status", keys: key("org_id", "to", "status")},
		{coll: "knowledge_benchmark_runs", name: "org_created", keys: key("org_id", "created_at")},
		{coll: "role_executions", name: "org_proposal", keys: key("org_id", "proposal_id")},
		// Sprint 6 collections (additive; the schema version stays 2).
		{coll: "extension_states", name: "org_ext", keys: key("org_id", "extension_id"), unique: true},
		{coll: "extension_installs", name: "org_ext_number", keys: key("org_id", "extension_id", "number"), unique: true},
		{coll: "extension_project_bindings", name: "org_prj_ext", keys: key("org_id", "project_id", "extension_id"), unique: true},
		// Sprint 7 collections (additive; the schema version stays 2).
		{coll: "knowledge_author_sessions", name: "org_dedupe", keys: key("org_id", "dedupe_key"), unique: true},
		{coll: "knowledge_author_sessions", name: "org_state_created", keys: key("org_id", "state", "created_at")},
		{coll: "knowledge_author_sessions", name: "org_run", keys: key("org_id", "run_id")},
		{coll: "code_indexes", name: "org_repo_commit", keys: key("org_id", "repo_key", "commit_sha"), unique: true},
		{coll: "code_indexes", name: "org_prj_state_finished", keys: key("org_id", "project_id", "state", "finished_at")},
		{coll: "backup_runs", name: "started", keys: key("started_at")},
		{coll: "security_scans", name: "org_run_head_created", keys: key("org_id", "run_id", "head_sha", "created_at")},
		{coll: "security_scans", name: "org_state_created", keys: key("org_id", "state", "created_at")},
		{coll: "security_findings", name: "org_scan_key", keys: key("org_id", "scan_id", "finding_key"), unique: true},
		{coll: "security_findings", name: "org_run_status", keys: key("org_id", "run_id", "status")},
		{coll: "skills", name: "org_slug", keys: key("org_id", "slug"), unique: true},
		{coll: "skill_versions", name: "org_skill_number", keys: key("org_id", "skill_id", "number"), unique: true},
		{coll: "skill_versions", name: "org_skill_draft", keys: key("org_id", "skill_id"), unique: true, partial: bson.M{"state": "draft"}},
		{coll: "skill_versions", name: "org_state", keys: key("org_id", "state")},
		{coll: "skill_binding_sets", name: "org_scope", keys: key("org_id", "scope_key"), unique: true},
	}

	for _, ix := range indexes {
		o := options.Index().SetName(ix.name)
		if ix.unique {
			o.SetUnique(true)
		}
		if ix.partial != nil {
			o.SetPartialFilterExpression(ix.partial)
		}
		if ix.ttl {
			o.SetExpireAfterSeconds(0)
		}

		if _, err := d.Coll(ix.coll).Indexes().CreateOne(ctx, mongo.IndexModel{Keys: ix.keys, Options: o}); err != nil {
			return err
		}
	}
	// The one compound text index: org_id and status are equality prefixes, so a query without them cannot use it
	// (K22a). default_language none tokenizes fa and en on whitespace and punctuation.
	textKeys := bson.D{{Key: "org_id", Value: 1}, {Key: "status", Value: 1}, {Key: "title", Value: "text"}, {Key: "claims.text", Value: "text"}, {Key: "body", Value: "text"}}
	if _, err := d.Coll("knowledge_versions").Indexes().CreateOne(ctx, mongo.IndexModel{Keys: textKeys, Options: options.Index().SetName("org_status_text").
		SetWeights(bson.M{"title": 5, "claims.text": 2, "body": 1}).SetDefaultLanguage("none")}); err != nil {
		return err
	}
	if _, err := d.Coll("counters").UpdateOne(ctx, bson.M{"_id": "events"}, bson.M{"$setOnInsert": bson.M{"seq": int64(0)}}, options.UpdateOne().SetUpsert(true)); err != nil {
		return err
	}
	_, err = d.Coll("schema_meta").UpdateOne(ctx, bson.M{"_id": "schema"}, bson.M{"$set": bson.M{"version": SchemaVersion, "updated_at": time.Now().UTC()}}, options.UpdateOne().SetUpsert(true))
	return err
}
func findOne[T any](ctx context.Context, d *DB, coll string, filter bson.M) (T, error) {
	var v T
	err := d.Coll(coll).FindOne(ctx, filter).Decode(&v)
	return v, err
}
func findMany[T any](ctx context.Context, d *DB, coll string, filter bson.M, sort bson.D, limit int64) ([]T, error) {
	c, err := d.Coll(coll).Find(ctx, filter, options.Find().SetSort(sort).SetLimit(limit))
	if err != nil {
		return nil, err
	}
	defer c.Close(ctx)
	v := []T{}
	err = c.All(ctx, &v)
	return v, err
}
func revisionUpdate[T interface{ DTO() bson.M }](ctx context.Context, d *DB, coll string, filter bson.M, revision int64, set bson.M) (T, error) {
	var v T
	set["updated_at"] = time.Now().UTC()
	withRevision := bson.M{}
	for k, x := range filter {
		withRevision[k] = x
	}
	withRevision["revision"] = revision
	err := d.Coll(coll).FindOneAndUpdate(ctx, withRevision, bson.M{"$set": set, "$inc": bson.M{"revision": int64(1)}}, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&v)
	if errors.Is(err, mongo.ErrNoDocuments) {
		current, e := findOne[T](ctx, d, coll, filter)
		if e != nil {
			return v, e
		}
		conflict := problem("revision_conflict")
		conflict.Current = current.DTO()
		return v, conflict
	}
	return v, err
}
