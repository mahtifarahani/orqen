"""Sprint 5 WP79: knowledge store routes. Run: python3 scripts/oas_s5_knowledge.py (idempotent)."""
import oas
oas.load()
for n in ['KnowledgeId', 'KnowledgeVersionId', 'KnowledgeConflictId']:
    oas.idschema(n)
S = oas.schema
S('KnowledgeScope', {'kind': 'enum:org|project|sprint|task', 'project_id': '?s', 'sprint_id': '?s', 'run_id': '?s'}, ['kind'])
S('KnowledgeScopeInput', {'kind': 'enum:org|project|sprint|task', 'project_id': 's', 'sprint_id': 's', 'run_id': 's'}, ['kind'])
S('KnowledgeEvidenceInput', {'ref_kind': 'enum:source_version|artifact|verification_evidence|review_finding|merge_request|run', 'ref_id': 's', 'locator': 's', 'excerpt': 's'}, ['ref_kind', 'ref_id'])
S('KnowledgeEvidence', {'ref_kind': 's', 'ref_id': 's', 'locator': 's', 'excerpt': 's', 'label': 's'}, ['ref_kind', 'ref_id'])
S('KnowledgeClaimInput', {'text': 's', 'template': '?obj', 'fact': '?obj', 'critical': 'b', 'evidence': '[$KnowledgeEvidenceInput]'}, ['text'])
S('KnowledgeClaim', {'id': 's', 'text': 's', 'template': '?obj', 'fact': '?obj', 'critical': 'b', 'verdict': 'enum:unchecked|supported|unsupported|stale', 'verdict_by': 's', 'evidence': '[$KnowledgeEvidence]'}, ['id', 'text', 'critical', 'verdict', 'evidence'])
S('KnowledgeAttestation', {'claim_id': 's', 'text': 's', 'by': 's', 'at': 's'})
S('KnowledgePromotion', {'mode': 'enum:user|auto', 'decided_by': 's', 'policy_version_id': '?s', 'at': 's', 'note': 's', 'attestations': '[$KnowledgeAttestation]'}, ['mode', 'at'])
S('KnowledgeVersion', {'id': '$KnowledgeVersionId', 'record_id': '$KnowledgeId', 'number': 'i32', 'type': 's', 'scope_key': 's', 'title': 's', 'body': 's', 'claims': '[$KnowledgeClaim]',
   'valid_from': '?s', 'valid_until': '?s', 'status': 'enum:draft|verified|conflicted|superseded|archived', 'supersedes': '?s', 'superseded_by': '?s', 'rollback_of': '?s', 'origin': 'obj',
   'promotion': '?$KnowledgePromotion', 'evidence_stale': 'b', 'injection_flags': '[s]', 'content_hash': 's', 'verified_at': '?s', 'created_at': 's'},
  ['id', 'record_id', 'number', 'type', 'scope_key', 'title', 'body', 'claims', 'status', 'origin', 'evidence_stale', 'injection_flags', 'content_hash', 'created_at'])
S('KnowledgeRecord', {'id': '$KnowledgeId', 'scope_key': 's', 'scope': '$KnowledgeScope', 'type': 's', 'key': 's', 'title': 's', 'current_version_id': 's', 'verified_version_id': '?s',
   'status': 'enum:draft|verified|conflicted|archived', 'pinned': 'b', 'critical': 'b', 'applies_to_roles': '[s]', 'active': 'b', 'archive_reason': '?s', 'revision': 'i64',
   'created_at': 's', 'updated_at': 's', 'current_version': '$KnowledgeVersion', 'verified_version': '$KnowledgeVersion'},
  ['id', 'scope_key', 'scope', 'type', 'key', 'title', 'current_version_id', 'status', 'pinned', 'critical', 'applies_to_roles', 'active', 'revision', 'created_at', 'updated_at'])
S('KnowledgeConflict', {'id': '$KnowledgeConflictId', 'version_ids': '[s]', 'record_ids': '[s]', 'pairs': '[obj]', 'state': 'enum:open|resolved', 'resolution': '?obj', 'revision': 'i64', 'created_at': 's', 'updated_at': 's'})
S('KnowledgeHit', {'version_id': 's', 'record_id': 's', 'title': 's', 'type': 's', 'scope_key': 's', 'score': 'num', 'snippet': 's'})
S('CreateKnowledgeRecordRequest', {'type': 'enum:business|project|sprint|task|experience', 'scope': '$KnowledgeScopeInput', 'key': 's', 'title': 's', 'body': 's', 'claims': '[$KnowledgeClaimInput]', 'valid_from': '?s', 'valid_until': '?s'}, ['type', 'scope', 'key', 'title', 'claims'])
S('CreateKnowledgeVersionRequest', {'base_version_id': 's', 'title': 's', 'body': 's', 'claims': '[$KnowledgeClaimInput]', 'valid_from': '?s', 'valid_until': '?s'}, ['title', 'claims'])
S('AttestationInput', {'claim_id': 's', 'text': 's'})
S('VerifyKnowledgeRequest', {'note': 's', 'attestations': '[$AttestationInput]'}, [])
S('RejectKnowledgeRequest', {'reason': 's'}, [])
S('RollbackKnowledgeRequest', {'to_version_id': 's', 'note': 's'}, ['to_version_id'])
S('RollbackKnowledgeResponse', {'outcome': 's', 'version': '$KnowledgeVersion'})
S('ArchiveKnowledgeRequest', {'reason': 's'}, [])
S('PinKnowledgeRequest', {'pinned': 'b', 'critical': 'b', 'applies_to_roles': '[s]'}, ['pinned'])
S('ResolveKnowledgeConflictRequest', {'choice': 'enum:keep_existing|accept_new|archive_both', 'note': 's', 'attestations': '[$AttestationInput]'}, ['choice'])
for n in ['KnowledgeRecord', 'KnowledgeVersion', 'KnowledgeConflict', 'KnowledgeHit']:
    oas.page(n)
K = ('knoId', 'KnowledgeId'); V = ('kvrId', 'KnowledgeVersionId'); C = ('kcfId', 'KnowledgeConflictId')
q = [('type', 's', ''), ('status', 's', ''), ('scope_key', 's', ''), ('project_id', 's', ''), ('include_archived', 'b', ''), ('limit', 'i32', ''), ('cursor', 's', '')]
op = oas.op
op('get', '/knowledge', 'list_knowledge', 'knowledge', resp='$Page_KnowledgeRecord', query=q)
op('post', '/knowledge', 'create_knowledge', 'knowledge', body='CreateKnowledgeRecordRequest', ok=201, resp='$KnowledgeRecord')
op('get', '/knowledge/{knoId}', 'get_knowledge', 'knowledge', params=[K], resp='$KnowledgeRecord')
op('get', '/knowledge/{knoId}/versions', 'list_knowledge_versions', 'knowledge', params=[K], resp='$Page_KnowledgeVersion')
op('post', '/knowledge/{knoId}/versions', 'create_knowledge_version', 'knowledge', params=[K], ifmatch=True, body='CreateKnowledgeVersionRequest', ok=201, resp='$KnowledgeVersion')
op('post', '/knowledge/{knoId}/rollback', 'rollback_knowledge', 'knowledge', params=[K], ifmatch=True, body='RollbackKnowledgeRequest', resp='$RollbackKnowledgeResponse')
op('post', '/knowledge/{knoId}/archive', 'archive_knowledge', 'knowledge', params=[K], ifmatch=True, body='ArchiveKnowledgeRequest', resp='$KnowledgeRecord')
op('put', '/knowledge/{knoId}/pin', 'pin_knowledge', 'knowledge', params=[K], ifmatch=True, body='PinKnowledgeRequest', resp='$KnowledgeRecord')
op('get', '/knowledge-versions/{kvrId}', 'get_knowledge_version', 'knowledge', params=[V], resp='$KnowledgeVersion')
op('post', '/knowledge-versions/{kvrId}/verify', 'verify_knowledge_version', 'knowledge', params=[V], ifmatch=True, body='VerifyKnowledgeRequest', resp='$KnowledgeVersion')
op('post', '/knowledge-versions/{kvrId}/reject', 'reject_knowledge_version', 'knowledge', params=[V], ifmatch=True, body='RejectKnowledgeRequest', resp='$KnowledgeVersion')
op('get', '/knowledge-conflicts', 'list_knowledge_conflicts', 'knowledge', resp='$Page_KnowledgeConflict', query=[('state', 's', ''), ('limit', 'i32', ''), ('cursor', 's', '')])
op('post', '/knowledge-conflicts/{kcfId}/resolve', 'resolve_knowledge_conflict', 'knowledge', params=[C], ifmatch=True, body='ResolveKnowledgeConflictRequest', resp='$KnowledgeConflict')
op('get', '/knowledge-search', 'search_knowledge', 'knowledge', resp='$Page_KnowledgeHit', query=[('q', 's', ''), ('type', 's', ''), ('project_id', 's', ''), ('limit', 'i32', '')])
oas.save()
