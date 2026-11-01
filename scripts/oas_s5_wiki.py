"""Sprint 5 WP81: wiki status and search result. Run: python3 scripts/oas_s5_wiki.py"""
import oas
oas.load()
oas.schema('KnowledgeSearchResult', {'mode': 'enum:wiki|fallback', 'degraded': '?s', 'items': '[$KnowledgeHit]'})
oas.schema('WikiStatus', {'configured': 'b', 'mode': 'enum:wiki|fallback', 'degraded': '?obj', 'pending': 'i64', 'applied_seq': 'i64', 'generation': 'i32', 'rebuild_requested': 'b'})
oas.op('get', '/knowledge-search', 'search_knowledge', 'knowledge', resp='$KnowledgeSearchResult', query=[('q', 's', ''), ('type', 's', ''), ('project_id', 's', ''), ('limit', 'i32', '')])
oas.op('get', '/wiki/status', 'get_wiki_status', 'knowledge', resp='$WikiStatus')
oas.op('post', '/wiki/maintain', 'start_wiki_maintain', 'knowledge', ok=202, resp='$WikiStatus')
oas.op('post', '/wiki/rebuild', 'start_wiki_rebuild', 'knowledge', ok=202, resp='$WikiStatus')
oas.save()
