"""Sprint 5 WP86: compaction, benchmark, retention preview, metrics. Run: python3 scripts/oas_s5_ops.py"""
import oas
oas.load()
oas.schema('KnowledgeBenchmarkRun', {'id': 's', 'created_at': 's', 'result': 'obj'})
oas.page('KnowledgeBenchmarkRun')
oas.schema('RetentionPreview', {'old_source_versions': 'i64', 'applied_outbox': 'i64', 'archived_versions': 'i64', 'stale_graph_edges': 'i64', 'cutoff': 's'})
oas.schema('KnowledgeMetrics', {'versions_by_status': 'obj', 'records': 'i64', 'stale_evidence': 'i64', 'open_conflicts': 'i64', 'proposals_waiting': 'i64', 'packets_served': 'i64', 'wiki_mode': 's', 'graph_edges': 'i64'})
oas.op('post', '/knowledge-compaction/run', 'run_knowledge_compaction', 'knowledge', ok=202, resp='$GraphQueued')
oas.op('post', '/knowledge-benchmarks', 'start_knowledge_benchmark', 'knowledge', ok=202, resp='$GraphQueued')
oas.op('get', '/knowledge-benchmarks', 'list_knowledge_benchmarks', 'knowledge', resp='$Page_KnowledgeBenchmarkRun')
oas.op('get', '/knowledge-retention/preview', 'get_retention_preview', 'knowledge', resp='$RetentionPreview')
oas.op('get', '/knowledge-metrics', 'get_knowledge_metrics', 'knowledge', resp='$KnowledgeMetrics')
oas.save()
