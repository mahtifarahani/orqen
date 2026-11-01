"""Sprint 5 WP83: knowledge proposals. Run: python3 scripts/oas_s5_proposals.py"""
import oas
oas.load()
oas.schema('KnowledgeProposalItem', {'record_id': 's', 'version_id': 's', 'key': 's', 'type': 's', 'outcome': 'enum:draft|verified|awaiting_review|conflicted|exists|skipped', 'reasons': '[s]'})
oas.schema('KnowledgeProposal', {'id': 's', 'run_id': '?s', 'trigger': 'enum:merge|manual|source_changed', 'bundle_artifact_id': '?s', 'state': 'enum:empty|awaiting_review|applied', 'items': '[$KnowledgeProposalItem]', 'created_at': 's'})
oas.page('KnowledgeProposal')
oas.op('post', '/runs/{runId}/knowledge/propose', 'propose_knowledge_from_run', 'knowledge', params=[('runId', 'RunId')], ok=201, resp='$KnowledgeProposal')
oas.op('get', '/knowledge-proposals', 'list_knowledge_proposals', 'knowledge', resp='$Page_KnowledgeProposal', query=[('run_id', 's', ''), ('limit', 'i32', ''), ('cursor', 's', '')])
oas.save()
