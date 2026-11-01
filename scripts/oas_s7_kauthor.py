"""Sprint 7 WP116: roles knowledge author sessions. Run: python3 scripts/oas_s7_kauthor.py"""
import oas
oas.load()
oas.idschema('KnowledgeAuthorSessionId')
ROLE = 'obj'
oas.schema('KauthorRoleDto', {'name': '?s', 'rung': '?s', 'status': '?s'})
oas.schema('KnowledgeAuthorSessionDto', {'id': '$KnowledgeAuthorSessionId', 'run_id': 's', 'attempt': 'i32', 'trigger': 'enum:merge|retry',
  'state': 'enum:queued|starting|writer_dispatching|writer_running|writer_done|kverify_dispatching|kverify_running|kverify_done|applied|failed|cancel_requested|cancelled',
  'code': '?s', 'writer': '$KauthorRoleDto', 'kverifier': '$KauthorRoleDto', 'proposal_id': '?s', 'fallback_proposal_id': '?s', 'created_at': 's', 'updated_at': 's', 'revision': 'i64'})
oas.schema('KnowledgeAuthorSessionList', {'items': '[$KnowledgeAuthorSessionDto]'})
S = ('kasId', 'KnowledgeAuthorSessionId')
oas.op('get', '/knowledge-author-sessions', 'list_knowledge_author_sessions', 'knowledge', resp='$KnowledgeAuthorSessionList', query=[('run_id', 's', '')])
oas.op('get', '/knowledge-author-sessions/{kasId}', 'get_knowledge_author_session', 'knowledge', params=[S], resp='$KnowledgeAuthorSessionDto')
oas.op('post', '/knowledge-author-sessions/{kasId}/cancel', 'cancel_knowledge_author_session', 'knowledge', params=[S], ifmatch=True, resp='$KnowledgeAuthorSessionDto')
oas.op('post', '/knowledge-author-sessions/{kasId}/retry', 'retry_knowledge_author_session', 'knowledge', params=[S], idem=True, ok=201, resp='$KnowledgeAuthorSessionDto')
oas.op('post', '/knowledge-author-sessions/{kasId}/fallback', 'fallback_knowledge_author_session', 'knowledge', params=[S], idem=True, resp='$KnowledgeProposal')
d = oas.doc['components']['schemas']
d['KnowledgeProposal']['properties']['trigger'] = oas.typ('enum:merge|merge_roles|manual|source_changed')
d['KnowledgeVersion']['properties']['role_verdicts'] = oas.typ('[$RoleVerdictDto]')
oas.schema('RoleVerdictDto', {'claim_id': 's', 'verdict': 'enum:supported|unsupported|contradicted|unchecked', 'reason': 's'})
oas.save()
