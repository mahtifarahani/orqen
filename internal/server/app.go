package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"io"
	"log/slog"
	"net"
	"net/http"
	"orqen/api"
	"orqen/internal/backup"
	"orqen/internal/catherd"
	"orqen/internal/codegraph"
	"orqen/internal/decision"
	"orqen/internal/execution"
	"orqen/internal/extensions"
	"orqen/internal/kindex"
	"orqen/internal/secscan"
	"orqen/internal/sysprobe"
	"orqen/web"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type App struct {
	Config      Config
	DB          *DB
	Creds       CredentialStore
	ctx         context.Context
	cancel      context.CancelFunc
	eventMu     sync.Mutex
	events      chan struct{}
	jobWake     chan struct{}
	hmu         sync.RWMutex
	handlers    map[string]JobHandler
	limMu       sync.Mutex
	Catherd     catherd.Port
	SysDeps     sysprobe.Deps
	profileHook func(stage string) error // test seam: runs inside the publish job
	CatherdRT   *catherd.Runtime
	orgLocks    sync.Map
	runnerBeat  atomic.Int64
	versionTry  atomic.Int64
	catalogMu   sync.Mutex
	catalogMem  map[string]catherd.Catalog
	limiters    map[string]chan struct{}
	secretBase  map[string]string                     // test hook: base URL per secret kind for the key check
	cooldown    sync.Map                              // connection id -> time.Time until which provider calls wait after a 429
	schedTick   time.Duration                         // scheduler tick (default 5 s); tests shorten it
	schedSweep  time.Duration                         // sweep interval (default 60 s)
	runHook     func(stage string) error              // test seam: called after a stage's external work, before its commit
	ruleHook    func([]PinnedRuleDoc) []PinnedRuleDoc // test seam: alters the rules loaded for a packet
	// Decision is the bounded decision adapter (Jev). Tests replace it with a decision.Fake through SetDecision.
	Decision decision.Port
	// Index is the KnowledgeIndex port (Wiki projection). Nil means not configured: knowledge search stays on the Mongo fallback.
	Index kindex.Index
	// CodeGraph replaces the GitNexus CLI adapter (tests). Nil means the CLI behind the gitnexus extension permit.
	CodeGraph codegraph.Index
	// Scanner replaces the Strix CLI adapter (tests).
	Scanner secscan.Scanner
	// backupNow is a clock seam for the backup schedule and retention (tests only).
	backupNow func() time.Time
	// Test seams of the workspace manager.
	gitHook          func(argv []string)
	allowFileRemotes bool
	statfs           func(path string) (uint64, error)
	// HostSupervisor owns the per-organization Codex app-servers (Sprint 4). Nil means no host is available.
	HostSupervisor    *execution.Supervisor
	dockerCheck       func() bool     // test seam: replaces the docker engine probe
	noSandbox         bool            // test seam: pretend sandbox-exec is missing
	ciPoll, ciMaxWait time.Duration   // test seams: replace ci.poll_interval_secs and ci.max_wait_secs
	reconcileDelays   []time.Duration // test seam: replaces the 2 s, 8 s, 30 s reconcile waits
	hostTurnTimeout   time.Duration   // test seam: replaces host.turn_timeout_secs
	hostResumed       sync.Map        // client pointer/thread -> true once the thread was resumed on that connection
}

func NewApp(c Config, db *DB, creds CredentialStore) *App {
	ctx, cancel := context.WithCancel(context.Background())
	a := &App{Config: c, DB: db, Creds: creds, ctx: ctx, cancel: cancel, events: make(chan struct{}), jobWake: make(chan struct{}, 1), handlers: map[string]JobHandler{}}
	a.schedTick, a.schedSweep = c.SchedTick, c.SchedSweep
	a.Decision = decision.NewJev(decision.Options{})
	a.registerBuiltinJobs()
	return a
}

// SetDecision replaces the decision adapter (tests).
func (a *App) SetDecision(p decision.Port) { a.Decision = p }
func (a *App) Stop()                       { a.cancel(); a.Committed() }

// UseCodexHost makes the real `codex app-server` (Config.CodexBin) the execution host. The production binary calls it
// once; tests inject their own supervisor instead.
func (a *App) UseCodexHost() {
	a.HostSupervisor = &execution.Supervisor{Spawn: execution.ExecSpawn, ClientVersion: Version, IdleStop: 10 * time.Minute}
}
func (a *App) Committed() {
	a.eventMu.Lock()
	close(a.events)
	a.events = make(chan struct{})
	a.eventMu.Unlock()
	select {
	case a.jobWake <- struct{}{}:
	default:
	}
}
func (a *App) eventSignal() <-chan struct{} {
	a.eventMu.Lock()
	defer a.eventMu.Unlock()
	return a.events
}

type contextKey int

const (
	requestIDKey contextKey = iota
	sessionKey
	orgKey
)

func requestID(r *http.Request) string { v, _ := r.Context().Value(requestIDKey).(string); return v }
func currentSession(r *http.Request) *SessionDoc {
	s, _ := r.Context().Value(sessionKey).(*SessionDoc)
	return s
}
func currentOrg(r *http.Request) OrganizationDoc { return r.Context().Value(orgKey).(OrganizationDoc) }
func contains(list []string, value string) bool {
	for _, v := range list {
		if v == value {
			return true
		}
	}
	return false
}
func safeMethod(method string) bool {
	return method == "GET" || method == "HEAD" || method == "OPTIONS"
}
func publicRoute(method, path string) bool {
	return method == "GET" && (path == "/api/v1/health" || path == "/api/v1/auth/status") || method == "POST" && (path == "/api/v1/auth/bootstrap" || path == "/api/v1/auth/redeem")
}

// idemRequired lists the routes whose handler calls idempotencyKey(r, true); TestContractRefs asserts the
// contract declares a required Idempotency-Key header on exactly these routes.
var idemRequired = map[string]bool{
	"POST /api/v1/orgs/{orgId}/connections/{conId}/test":                   true,
	"POST /api/v1/orgs/{orgId}/connections/{conId}/catalog/refresh":        true,
	"POST /api/v1/orgs/{orgId}/projects/from-catalog":                      true,
	"POST /api/v1/orgs/{orgId}/projects/{prjId}/mapping/check":             true,
	"POST /api/v1/orgs/{orgId}/projects/{prjId}/sources/sync":              true,
	"POST /api/v1/orgs/{orgId}/projects/{prjId}/contract/import":           true,
	"POST /api/v1/orgs/{orgId}/policy/publish":                             true,
	"POST /api/v1/orgs/{orgId}/catherd/doctor":                             true,
	"POST /api/v1/orgs/{orgId}/system/preflight":                           true,
	"POST /api/v1/orgs/{orgId}/projects/{prjId}/automation/enable":         true,
	"POST /api/v1/orgs/{orgId}/model-profiles/import-personal":             true,
	"POST /api/v1/orgs/{orgId}/model-profiles/{mpfId}/validate":            true,
	"POST /api/v1/orgs/{orgId}/model-profiles/{mpfId}/publish":             true,
	"POST /api/v1/orgs/{orgId}/model-profiles/{mpfId}/rollback":            true,
	"POST /api/v1/orgs/{orgId}/system/keychain-probe":                      true,
	"POST /api/v1/orgs/{orgId}/catherd/catalog/refresh":                    true,
	"POST /api/v1/orgs/{orgId}/projects/{prjId}/policy/publish":            true,
	"POST /api/v1/orgs/{orgId}/catherd/secrets/{kind}/check":               true,
	"POST /api/v1/orgs/{orgId}/tasks/{issId}/evaluate":                     true,
	"POST /api/v1/orgs/{orgId}/projects/{prjId}/intake/poll":               true,
	"POST /api/v1/orgs/{orgId}/runs/{runId}/start":                         true,
	"POST /api/v1/orgs/{orgId}/runs/{runId}/resume":                        true,
	"POST /api/v1/orgs/{orgId}/runs/{runId}/cancel":                        true,
	"POST /api/v1/orgs/{orgId}/runs/{runId}/retry":                         true,
	"POST /api/v1/orgs/{orgId}/inbox/{inbId}/answer":                       true,
	"POST /api/v1/orgs/{orgId}/inbox/{inbId}/resolve":                      true,
	"POST /api/v1/orgs/{orgId}/workspaces/{wspId}/discard":                 true,
	"POST /api/v1/orgs/{orgId}/connections/{conId}/write-credential/check": true,
	"POST /api/v1/orgs/{orgId}/runs/{runId}/external-operations/approve":   true,
	"POST /api/v1/orgs/{orgId}/runs/{runId}/findings/{fndId}/waive":        true,
	"POST /api/v1/orgs/{orgId}/merge-requests/{mrqId}/refresh":             true,
	"POST /api/v1/orgs/{orgId}/host/probe":                                 true,
}

type endpoint func(http.ResponseWriter, *http.Request) error
type Route struct {
	Method, Path string
	Handler      endpoint
}

func (a *App) Routes() []Route {
	return append([]Route{
		{"GET", "/api/v1/health", a.health}, {"GET", "/api/v1/auth/status", a.authStatus},
		{"POST", "/api/v1/auth/bootstrap", a.consumeAuth}, {"POST", "/api/v1/auth/redeem", a.consumeAuth}, {"POST", "/api/v1/auth/logout", a.logout},
		{"GET", "/api/v1/orgs", a.listOrgs}, {"POST", "/api/v1/orgs", a.createOrg},
		{"GET", "/api/v1/orgs/{orgId}", a.getOrg}, {"PATCH", "/api/v1/orgs/{orgId}", a.patchOrg},
		{"GET", "/api/v1/orgs/{orgId}/connections", a.listConnections}, {"POST", "/api/v1/orgs/{orgId}/connections", a.createConnection},
		{"GET", "/api/v1/orgs/{orgId}/connections/{conId}", a.getConnection}, {"PATCH", "/api/v1/orgs/{orgId}/connections/{conId}", a.patchConnection}, {"DELETE", "/api/v1/orgs/{orgId}/connections/{conId}", a.archiveConnection},
		{"PUT", "/api/v1/orgs/{orgId}/connections/{conId}/credential", a.putCredential}, {"DELETE", "/api/v1/orgs/{orgId}/connections/{conId}/credential", a.deleteCredential},
		{"POST", "/api/v1/orgs/{orgId}/connections/{conId}/test", a.testConnection},
		{"GET", "/api/v1/orgs/{orgId}/connections/{conId}/lookup/jira-projects", a.lookupJiraProjects}, {"GET", "/api/v1/orgs/{orgId}/connections/{conId}/lookup/jira-boards", a.lookupJiraBoards}, {"GET", "/api/v1/orgs/{orgId}/connections/{conId}/lookup/gitlab-branches", a.lookupGitLabBranches},
		{"POST", "/api/v1/orgs/{orgId}/connections/{conId}/catalog/refresh", a.refreshCatalog}, {"GET", "/api/v1/orgs/{orgId}/connections/{conId}/catalog-projects", a.listCatalogProjects}, {"GET", "/api/v1/orgs/{orgId}/catalog/summary", a.catalogSummary},
		{"GET", "/api/v1/orgs/{orgId}/projects", a.listProjects}, {"POST", "/api/v1/orgs/{orgId}/projects/from-catalog", a.fromCatalog},
		{"GET", "/api/v1/orgs/{orgId}/projects/{prjId}", a.getProject}, {"PATCH", "/api/v1/orgs/{orgId}/projects/{prjId}", a.patchProject}, {"DELETE", "/api/v1/orgs/{orgId}/projects/{prjId}", a.archiveProject},
		{"PUT", "/api/v1/orgs/{orgId}/projects/{prjId}/mapping", a.putMapping}, {"POST", "/api/v1/orgs/{orgId}/projects/{prjId}/mapping/check", a.startMappingCheck},
		{"PUT", "/api/v1/orgs/{orgId}/projects/{prjId}/commands", a.putCommands}, {"PUT", "/api/v1/orgs/{orgId}/projects/{prjId}/protected-paths", a.putProtectedPaths},
		{"GET", "/api/v1/orgs/{orgId}/projects/{prjId}/readiness", a.getProjectReadiness},
		{"POST", "/api/v1/orgs/{orgId}/projects/{prjId}/contract/import", a.startContractImport}, {"GET", "/api/v1/orgs/{orgId}/projects/{prjId}/contract/snapshots", a.listContractSnapshots},
		{"GET", "/api/v1/orgs/{orgId}/projects/{prjId}/contract/snapshots/{rcsId}", a.getContractSnapshot}, {"POST", "/api/v1/orgs/{orgId}/projects/{prjId}/contract/snapshots/{rcsId}/review", a.reviewContract},
		{"GET", "/api/v1/orgs/{orgId}/policy/schema", a.getPolicySchema},
		{"GET", "/api/v1/orgs/{orgId}/policy/draft", a.scoped(false, a.getPolicyDraft)}, {"PUT", "/api/v1/orgs/{orgId}/policy/draft", a.scoped(false, a.putPolicyDraft)}, {"POST", "/api/v1/orgs/{orgId}/policy/publish", a.scoped(false, a.publishPolicy)},
		{"GET", "/api/v1/orgs/{orgId}/policy/versions", a.scoped(false, a.listPolicyVersions)}, {"GET", "/api/v1/orgs/{orgId}/policy/versions/{polId}", a.scoped(false, a.getPolicyVersion)}, {"GET", "/api/v1/orgs/{orgId}/policy/effective", a.scoped(false, a.getPolicyEffective)},
		{"GET", "/api/v1/orgs/{orgId}/projects/{prjId}/policy/draft", a.scoped(true, a.getPolicyDraft)}, {"PUT", "/api/v1/orgs/{orgId}/projects/{prjId}/policy/draft", a.scoped(true, a.putPolicyDraft)}, {"POST", "/api/v1/orgs/{orgId}/projects/{prjId}/policy/publish", a.scoped(true, a.publishPolicy)},
		{"GET", "/api/v1/orgs/{orgId}/projects/{prjId}/policy/versions", a.scoped(true, a.listPolicyVersions)}, {"GET", "/api/v1/orgs/{orgId}/projects/{prjId}/policy/versions/{polId}", a.scoped(true, a.getPolicyVersion)}, {"GET", "/api/v1/orgs/{orgId}/projects/{prjId}/policy/effective", a.scoped(true, a.getPolicyEffective)},
		{"GET", "/api/v1/orgs/{orgId}/catherd/status", a.getCatherdStatus}, {"GET", "/api/v1/orgs/{orgId}/catherd/schema", a.getCatherdSchema}, {"POST", "/api/v1/orgs/{orgId}/catherd/doctor", a.startDoctor}, {"GET", "/api/v1/orgs/{orgId}/catherd/hosts", a.listOrchestratorHosts},
		{"POST", "/api/v1/orgs/{orgId}/catherd/catalog/refresh", a.startCatherdCatalog}, {"GET", "/api/v1/orgs/{orgId}/catherd/catalog", a.getCatherdCatalog}, {"GET", "/api/v1/orgs/{orgId}/catherd/personal-profiles", a.getPersonalProfiles},
		{"GET", "/api/v1/orgs/{orgId}/catherd/secrets", a.listCatherdSecrets}, {"PUT", "/api/v1/orgs/{orgId}/catherd/secrets/{kind}", a.putCatherdSecret}, {"DELETE", "/api/v1/orgs/{orgId}/catherd/secrets/{kind}", a.deleteCatherdSecret}, {"POST", "/api/v1/orgs/{orgId}/catherd/secrets/{kind}/check", a.checkCatherdSecret},
		{"GET", "/api/v1/orgs/{orgId}/knowledge", a.listKnowledge}, {"POST", "/api/v1/orgs/{orgId}/knowledge", a.createKnowledge},
		{"GET", "/api/v1/orgs/{orgId}/knowledge/{knoId}", a.getKnowledge}, {"GET", "/api/v1/orgs/{orgId}/knowledge/{knoId}/versions", a.listKnowledgeVersions}, {"POST", "/api/v1/orgs/{orgId}/knowledge/{knoId}/versions", a.createKnowledgeVersionRoute},
		{"POST", "/api/v1/orgs/{orgId}/knowledge/{knoId}/rollback", a.rollbackKnowledge}, {"POST", "/api/v1/orgs/{orgId}/knowledge/{knoId}/archive", a.archiveKnowledge}, {"PUT", "/api/v1/orgs/{orgId}/knowledge/{knoId}/pin", a.pinKnowledge},
		{"GET", "/api/v1/orgs/{orgId}/knowledge-versions/{kvrId}", a.getKnowledgeVersion}, {"POST", "/api/v1/orgs/{orgId}/knowledge-versions/{kvrId}/verify", a.verifyKnowledgeVersion}, {"POST", "/api/v1/orgs/{orgId}/knowledge-versions/{kvrId}/reject", a.rejectKnowledgeVersion},
		{"GET", "/api/v1/orgs/{orgId}/knowledge-conflicts", a.listKnowledgeConflicts}, {"POST", "/api/v1/orgs/{orgId}/knowledge-conflicts/{kcfId}/resolve", a.resolveKnowledgeConflict},
		{"GET", "/api/v1/orgs/{orgId}/knowledge-search", a.searchKnowledge},
		{"GET", "/api/v1/orgs/{orgId}/runs/{runId}/context/roles/{role}", a.getRoleContext},
		{"POST", "/api/v1/orgs/{orgId}/runs/{runId}/knowledge/propose", a.proposeFromRun}, {"GET", "/api/v1/orgs/{orgId}/knowledge-proposals", a.listProposals},
		{"POST", "/api/v1/orgs/{orgId}/graph/rebuild", a.startGraphRebuild}, {"GET", "/api/v1/orgs/{orgId}/graph/nodes", a.listGraphNodes}, {"GET", "/api/v1/orgs/{orgId}/graph/neighborhood", a.graphNeighborhood},
		{"POST", "/api/v1/orgs/{orgId}/graph/edges", a.createGraphEdge}, {"POST", "/api/v1/orgs/{orgId}/graph/edges/{gedId}/confirm", a.setEdgeStatus("verified")}, {"POST", "/api/v1/orgs/{orgId}/graph/edges/{gedId}/reject", a.setEdgeStatus("rejected")},
		{"POST", "/api/v1/orgs/{orgId}/knowledge-compaction/run", a.startKnowledgeOp("knowledge_compaction")}, {"POST", "/api/v1/orgs/{orgId}/knowledge-benchmarks", a.startKnowledgeOp("knowledge_benchmark")}, {"GET", "/api/v1/orgs/{orgId}/knowledge-benchmarks", a.listBenchmarks},
		{"GET", "/api/v1/orgs/{orgId}/knowledge-retention/preview", a.retentionPreview}, {"GET", "/api/v1/orgs/{orgId}/knowledge-metrics", a.knowledgeMetrics},
		{"GET", "/api/v1/orgs/{orgId}/wiki/status", a.getWikiStatus}, {"POST", "/api/v1/orgs/{orgId}/wiki/maintain", a.startWikiMaintain}, {"POST", "/api/v1/orgs/{orgId}/wiki/rebuild", a.startWikiRebuild},
		{"GET", "/api/v1/orgs/{orgId}/sources", a.listSources}, {"GET", "/api/v1/orgs/{orgId}/sources/{srvId}", a.getSource}, {"POST", "/api/v1/orgs/{orgId}/projects/{prjId}/sources/sync", a.startSourceSync},
		{"GET", "/api/v1/orgs/{orgId}/health", a.getOrgHealth},
		{"POST", "/api/v1/orgs/{orgId}/system/preflight", a.startPreflight}, {"GET", "/api/v1/orgs/{orgId}/system/preflight", a.getPreflight}, {"POST", "/api/v1/orgs/{orgId}/system/keychain-probe", a.probeKeychain},
		{"GET", "/api/v1/orgs/{orgId}/system/launchd", a.getLaunchdPlan}, {"PATCH", "/api/v1/orgs/{orgId}/system/launchd", a.patchLaunchd},
		{"GET", "/api/v1/orgs/{orgId}/model-profiles", a.listProfiles}, {"POST", "/api/v1/orgs/{orgId}/model-profiles", a.createProfile}, {"POST", "/api/v1/orgs/{orgId}/model-profiles/import-personal", a.startImportPersonal},
		{"GET", "/api/v1/orgs/{orgId}/model-profiles/{mpfId}", a.getProfile}, {"PATCH", "/api/v1/orgs/{orgId}/model-profiles/{mpfId}", a.patchProfile}, {"DELETE", "/api/v1/orgs/{orgId}/model-profiles/{mpfId}", a.archiveProfile},
		{"PUT", "/api/v1/orgs/{orgId}/model-profiles/{mpfId}/draft", a.putProfileDraft}, {"GET", "/api/v1/orgs/{orgId}/model-profiles/{mpfId}/draft/issues", a.getDraftIssues},
		{"POST", "/api/v1/orgs/{orgId}/model-profiles/{mpfId}/validate", a.startProfileValidation}, {"GET", "/api/v1/orgs/{orgId}/model-profiles/{mpfId}/diff", a.getProfileDiff},
		{"POST", "/api/v1/orgs/{orgId}/model-profiles/{mpfId}/publish", a.publishProfile}, {"POST", "/api/v1/orgs/{orgId}/model-profiles/{mpfId}/rollback", a.rollbackProfile},
		{"GET", "/api/v1/orgs/{orgId}/model-profiles/{mpfId}/versions", a.listProfileVersions}, {"GET", "/api/v1/orgs/{orgId}/model-profile-versions/{mpvId}", a.getProfileVersion},
		{"PUT", "/api/v1/orgs/{orgId}/projects/{prjId}/profile-binding", a.putProfileBinding}, {"POST", "/api/v1/orgs/{orgId}/projects/{prjId}/automation/enable", a.enableAutomation}, {"POST", "/api/v1/orgs/{orgId}/projects/{prjId}/automation/disable", a.disableAutomation},
		{"GET", "/api/v1/orgs/{orgId}/setup", a.getSetup}, {"PATCH", "/api/v1/orgs/{orgId}/setup", a.patchSetup}, {"POST", "/api/v1/orgs/{orgId}/setup/finish", a.finishSetup},
		{"POST", "/api/v1/orgs/{orgId}/setup/steps/{stepKey}/complete", a.stepAction("complete")}, {"POST", "/api/v1/orgs/{orgId}/setup/steps/{stepKey}/skip", a.stepAction("skip")}, {"POST", "/api/v1/orgs/{orgId}/setup/steps/{stepKey}/reopen", a.stepAction("reopen")},
		{"GET", "/api/v1/orgs/{orgId}/tasks", a.listTasks}, {"GET", "/api/v1/orgs/{orgId}/tasks/summary", a.getTaskSummary}, {"GET", "/api/v1/orgs/{orgId}/tasks/{issId}", a.getTask}, {"POST", "/api/v1/orgs/{orgId}/tasks/{issId}/evaluate", a.evaluateTask},
		{"POST", "/api/v1/orgs/{orgId}/projects/{prjId}/intake/poll", a.pollProjectNow}, {"GET", "/api/v1/orgs/{orgId}/projects/{prjId}/intake", a.getProjectIntake},
		{"GET", "/api/v1/orgs/{orgId}/runs/{runId}", a.getRun}, {"GET", "/api/v1/orgs/{orgId}/runs/{runId}/timeline", a.listRunTimeline}, {"GET", "/api/v1/orgs/{orgId}/runs/{runId}/context", a.getRunContext}, {"GET", "/api/v1/orgs/{orgId}/runs/{runId}/roles", a.listRunRoles}, {"GET", "/api/v1/orgs/{orgId}/runs/{runId}/verification", a.listRunVerification}, {"GET", "/api/v1/orgs/{orgId}/merge-requests", a.listMergeRequests}, {"GET", "/api/v1/orgs/{orgId}/merge-requests/{mrqId}", a.getMergeRequest}, {"POST", "/api/v1/orgs/{orgId}/merge-requests/{mrqId}/refresh", a.refreshMergeRequest}, {"GET", "/api/v1/orgs/{orgId}/runs/{runId}/findings", a.listRunFindings}, {"POST", "/api/v1/orgs/{orgId}/runs/{runId}/findings/{fndId}/waive", a.waiveFinding}, {"GET", "/api/v1/orgs/{orgId}/runs/{runId}/external-operations", a.listRunExternalOperations}, {"POST", "/api/v1/orgs/{orgId}/runs/{runId}/external-operations/approve", a.approveOperations}, {"GET", "/api/v1/orgs/{orgId}/external-operations/{xopId}", a.getExternalOperation}, {"GET", "/api/v1/orgs/{orgId}/runs/{runId}/diff", a.getRunDiff},
		{"POST", "/api/v1/orgs/{orgId}/runs/{runId}/start", a.startRun}, {"POST", "/api/v1/orgs/{orgId}/runs/{runId}/resume", a.resumeRun}, {"POST", "/api/v1/orgs/{orgId}/runs/{runId}/cancel", a.cancelRun}, {"POST", "/api/v1/orgs/{orgId}/runs/{runId}/retry", a.retryRun},
		{"GET", "/api/v1/orgs/{orgId}/inbox", a.listInbox}, {"GET", "/api/v1/orgs/{orgId}/inbox/{inbId}", a.getInboxItem}, {"POST", "/api/v1/orgs/{orgId}/inbox/{inbId}/answer", a.answerInboxItem}, {"POST", "/api/v1/orgs/{orgId}/inbox/{inbId}/resolve", a.resolveInboxItem},
		{"POST", "/api/v1/orgs/{orgId}/workspaces/{wspId}/discard", a.discardWorkspace},
		{"GET", "/api/v1/orgs/{orgId}/host", a.getHost}, {"POST", "/api/v1/orgs/{orgId}/host/probe", a.probeHost}, {"POST", "/api/v1/orgs/{orgId}/host/spike-attest", a.spikeAttest},
		{"GET", "/api/v1/orgs/{orgId}/external-writes", a.getExternalWriteSettings}, {"PUT", "/api/v1/orgs/{orgId}/external-writes", a.putExternalWriteSettings}, {"PUT", "/api/v1/orgs/{orgId}/external-writes/pause", a.putExternalWritesPause},
		{"GET", "/api/v1/orgs/{orgId}/connections/{conId}/write-credential", a.getWriteCredential}, {"PUT", "/api/v1/orgs/{orgId}/connections/{conId}/write-credential", a.putWriteCredential}, {"DELETE", "/api/v1/orgs/{orgId}/connections/{conId}/write-credential", a.deleteWriteCredential}, {"POST", "/api/v1/orgs/{orgId}/connections/{conId}/write-credential/check", a.checkWriteCredential},
		{"GET", "/api/v1/orgs/{orgId}/rules", a.listPinnedRules}, {"POST", "/api/v1/orgs/{orgId}/rules", a.createPinnedRule}, {"PATCH", "/api/v1/orgs/{orgId}/rules/{rulId}", a.patchPinnedRule}, {"DELETE", "/api/v1/orgs/{orgId}/rules/{rulId}", a.archivePinnedRule},
		{"GET", "/api/v1/orgs/{orgId}/automation", a.getAutomationState}, {"PUT", "/api/v1/orgs/{orgId}/automation", a.putAutomationState},
		{"GET", "/api/v1/orgs/{orgId}/operations/{opId}", a.getOperation},
		{"GET", "/api/v1/orgs/{orgId}/artifacts/{artId}", a.getArtifact}, {"GET", "/api/v1/orgs/{orgId}/artifacts/{artId}/content", a.getArtifactContent},
		{"GET", "/api/v1/orgs/{orgId}/events", a.listEvents}, {"GET", "/api/v1/orgs/{orgId}/events/stream", a.streamEvents},
	}, append(a.s6Routes(), a.s7Routes()...)...)
}
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, route := range a.Routes() {
		mux.Handle(route.Method+" "+route.Path, a.apiMiddleware(route.Handler))
	}
	mux.HandleFunc("GET /api/v1/openapi.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(api.OpenAPI)
	})
	mux.Handle("GET /api/docs", http.HandlerFunc(serveDocs))
	mux.Handle("GET /api/docs/", http.HandlerFunc(serveDocs))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
			writeError(w, r, problem("not_found"))
			return
		}
		web.ServeUI(w, r, a.Config.Dev)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if len(id) < 1 || len(id) > 64 || strings.IndexFunc(id, func(c rune) bool {
			return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_')
		}) >= 0 {
			id = newID("req")
		}
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey, id))
		w.Header().Set("X-Request-ID", id)
		if !contains(a.Config.AllowedHosts(), strings.ToLower(r.Host)) {
			writeError(w, r, problem("host_rejected"))
			return
		}
		if r.ContentLength > 1024*1024 {
			writeError(w, r, problem("payload_too_large"))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1024*1024)
		mux.ServeHTTP(w, r)
	})
}
func (a *App) apiMiddleware(next endpoint) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !safeMethod(r.Method) && !contains(a.Config.AllowedOrigins(), r.Header.Get("Origin")) {
			writeError(w, r, problem("origin_rejected"))
			return
		}
		var s *SessionDoc
		if c, err := r.Cookie(a.Config.CookieName()); err == nil {
			var e error
			s, e = a.loadSession(r.Context(), c.Value)
			if e != nil {
				writeError(w, r, e)
				return
			}
		}
		if s == nil && !publicRoute(r.Method, r.URL.Path) {
			writeError(w, r, problem("unauthenticated"))
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), sessionKey, s))
		if s != nil && !safeMethod(r.Method) && r.URL.Path != "/api/v1/auth/bootstrap" && r.URL.Path != "/api/v1/auth/redeem" && subtle.ConstantTimeCompare([]byte(s.CsrfToken), []byte(r.Header.Get("X-CSRF-Token"))) != 1 {
			writeError(w, r, problem("csrf_failed"))
			return
		}
		if org := r.PathValue("orgId"); org != "" {
			if !validID("org", org) {
				writeError(w, r, problem("not_found"))
				return
			}
			doc, err := findOne[OrganizationDoc](r.Context(), a.DB, "organizations", bson.M{"_id": org, "archived_at": nil})
			if err != nil {
				writeError(w, r, err)
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), orgKey, doc))
		}
		for _, p := range []struct{ key, prefix string }{{"conId", "con"}, {"opId", "op"}, {"artId", "art"}, {"prjId", "prj"}, {"rcsId", "rcs"}, {"polId", "pol"}, {"mpfId", "mpf"}, {"mpvId", "mpv"}, {"issId", "iss"}, {"runId", "run"}, {"inbId", "inb"}, {"wspId", "wsp"}, {"rulId", "rul"}, {"fndId", "fnd"}, {"xopId", "xop"}, {"mrqId", "mrq"}, {"srvId", "srv"}, {"knoId", "kno"}, {"kvrId", "kvr"}, {"kprId", "kpr"}, {"kcfId", "kcf"}, {"gnoId", "gno"}, {"gedId", "ged"}, {"kbrId", "kbr"}, {"rexId", "rex"}, {"sklId", "skl"}, {"skvId", "skv"}, {"cixId", "cix"}, {"scnId", "scn"}, {"sfdId", "sfd"}, {"kasId", "kas"}} {
			if id := r.PathValue(p.key); id != "" && !validID(p.prefix, id) {
				writeError(w, r, problem("not_found"))
				return
			}
		}
		if n := r.PathValue("backupName"); n != "" && !backup.NamePattern.MatchString(n) {
			writeError(w, r, problem("not_found"))
			return
		}
		if id := r.PathValue("extId"); id != "" {
			if _, ok := extensions.Get(id); !ok {
				writeError(w, r, problem("not_found"))
				return
			}
		}
		if err := next(w, r); err != nil {
			writeError(w, r, err)
		}
	})
}
func decodeJSON(w http.ResponseWriter, r *http.Request, target any, strict bool) error {
	contentType := strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])
	if contentType != "application/json" && !strings.HasSuffix(contentType, "+json") {
		return validation("body", "expected application/json")
	}
	dec := json.NewDecoder(r.Body)
	if strict {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(target); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return problem("payload_too_large")
		}
		return validation("body", err.Error())
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return problem("payload_too_large")
		}
		return validation("body", "expected one JSON object")
	}
	return nil
}
func (a *App) health(w http.ResponseWriter, r *http.Request) error {
	name, err := a.DB.ReplicaSet(r.Context())
	ok := err == nil
	status := "ok"
	if !ok {
		status = "degraded"
	}
	var replica any
	if ok {
		replica = name
	}
	writeJSON(w, 200, bson.M{"status": status, "version": Version, "schema_version": SchemaVersion, "mongo": bson.M{"ok": ok, "replica_set": replica}})
	return nil
}

// Run owns both HTTP and worker lifetimes. Cancellation closes SSE and aborts in-flight jobs.
func (a *App) Run(ctx context.Context, listener net.Listener, startRunner bool) error {
	if a.HostSupervisor != nil {
		defer a.HostSupervisor.StopAll() // no app-server outlives orqen
	}
	if startRunner {
		if err := checkRestoreLock(ctx, a.DB); err != nil {
			return err
		}
	}
	if err := a.EnsureBootstrap(ctx); err != nil {
		return err
	}
	if err := a.sweepAbandoned(ctx, "", ""); err != nil {
		slog.Warn("could not sweep abandoned profile publishes", "error", err)
	}
	done := make(chan struct{})
	if startRunner {
		go func() { defer close(done); a.runWorkers() }()
		go a.runScheduler()
		go a.runnerBeatLoop()
	} else {
		close(done)
	}
	srv := &http.Server{Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(listener) }()
	var err error
	select {
	case <-ctx.Done():
	case err = <-served:
	}
	a.Stop()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if e := srv.Shutdown(shutdownCtx); e != nil {
		slog.Warn("HTTP shutdown", "error", e)
		_ = srv.Close()
	}
	select {
	case <-done:
	case <-shutdownCtx.Done():
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func etag(revision int64) string { return fmt.Sprintf("\"%d\"", revision) }

// s7Routes lists the Sprint 7 routes.
func (a *App) s7Routes() []Route { return a.kauthorRoutes() }

// s6Routes lists the Sprint 6 routes of the extension, skill, code graph and security areas.
func (a *App) s6Routes() []Route {
	var out []Route
	for _, part := range [][]Route{a.extensionRoutes(), a.skillRoutes(), a.codeGraphRoutes(), a.securityRoutes(), a.backupRoutes()} {
		out = append(out, part...)
	}
	return out
}
