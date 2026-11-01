"""Sprint 5 WP80: source routes. Run: python3 scripts/oas_s5_sources.py"""
import oas
oas.load()
oas.idschema('SourceVersionId')
oas.schema('SourceVersion', {'id': 's', 'kind': 's', 'connection_id': '?s', 'external_id': 's', 'url': 's', 'title': 's', 'remote_version': 'i32', 'content_hash': 's', 'artifact_id': '?s', 'bytes': 'i32',
  'truncated': 'b', 'link_only': 'b', 'authority': 's', 'fetched_at': 's', 'prev_id': '?s', 'latest': 'b', 'project_id': '?s', 'text_preview': 's'},
  ['id', 'kind', 'external_id', 'title', 'content_hash', 'bytes', 'truncated', 'link_only', 'authority', 'fetched_at', 'latest'])
oas.page('SourceVersion')
oas.op('get', '/sources', 'list_sources', 'knowledge', resp='$Page_SourceVersion', query=[('kind', 's', ''), ('latest', 'b', ''), ('project_id', 's', ''), ('limit', 'i32', ''), ('cursor', 's', '')])
oas.op('get', '/sources/{srvId}', 'get_source', 'knowledge', params=[('srvId', 'SourceVersionId')], resp='$SourceVersion')
oas.op('post', '/projects/{prjId}/sources/sync', 'start_source_sync', 'knowledge', params=[('prjId', 'ProjectId')], idem=True, ok=202, resp='$OperationDto')
oas.save()
