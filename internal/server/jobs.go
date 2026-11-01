package server

import (
	"context"
	"sort"
)

type JobHandler func(ctx context.Context, job JobDoc, lease Lease) error

// Handle registers or replaces the handler for a job kind. Tests may override built-ins.
func (a *App) Handle(kind string, h JobHandler) {
	a.hmu.Lock()
	defer a.hmu.Unlock()
	a.handlers[kind] = h
}
func (a *App) handler(kind string) JobHandler {
	a.hmu.RLock()
	defer a.hmu.RUnlock()
	return a.handlers[kind]
}
func (a *App) handlerKinds() []string {
	a.hmu.RLock()
	defer a.hmu.RUnlock()
	kinds := make([]string, 0, len(a.handlers))
	for k := range a.handlers {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	return kinds
}
func (a *App) registerBuiltinJobs() {
	a.Handle("connection_test", a.runConnectionTest)
	a.Handle("catalog_refresh", a.runCatalogRefresh)
	a.Handle("mapping_check", a.runMappingCheck)
	a.Handle("contract_import", a.runContractImport)
	a.Handle("system_preflight", a.runPreflight)
	a.Handle("profile_validate", a.runProfileValidate)
	a.Handle("profile_publish", a.runProfilePublish)
	a.Handle("profile_import_personal", a.runProfileImport)
	a.Handle("catherd_doctor", a.runCatherdDoctor)
	a.Handle("catherd_catalog", a.runCatherdCatalog)
	a.Handle("catherd_secret_check", a.runCatherdSecretCheck)
	a.Handle("intake_poll", a.runIntakePoll)
	a.Handle("issue_ingest", a.runIssueIngest)
	a.Handle("run_advance", a.runAdvance)
	a.Handle("workspace_cleanup", a.runWorkspaceCleanup)
	a.Handle("workspace_discard", a.runWorkspaceDiscard)
	a.Handle("role_watch", a.runRoleWatch)
	a.Handle("role_cancel", a.runRoleCancel)
	a.Handle("mr_watch", a.runMRWatch)
	a.Handle("mr_refresh", a.runMRRefresh)
	a.Handle("write_credential_check", a.runWriteCredentialCheck)
	a.Handle("host_probe", a.runHostProbe)
	a.Handle("source_sync", a.runSourceSync)
	a.Handle("wiki_maintain", a.runWikiMaintain)
	a.Handle("knowledge_author", a.runKnowledgeAuthor)
	a.Handle(kauthorJobKind, a.runKnowledgeAuthorRoles)
	a.Handle("graph_build", a.runGraphBuild)
	a.Handle("knowledge_compaction", a.runKnowledgeCompaction)
	a.Handle("knowledge_benchmark", a.runKnowledgeBenchmark)
	a.Handle("backup_run", a.runBackup)
	a.Handle("post_restore_check", a.runPostRestoreCheck)
	a.Handle("extension_detect", a.runExtensionDetect)
	a.Handle("extension_test", a.runExtensionTest)
	a.Handle("extension_health", a.runExtensionHealth)
	a.Handle("code_index", a.runCodeIndex)
	a.Handle("security_scan", a.runSecurityScan)
}
