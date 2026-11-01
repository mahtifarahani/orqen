"""Sprint 6 WP102: security scan routes. Run: python3 scripts/oas_s6_security.py"""
import oas
oas.load()
oas.idschema('SecurityScanId')
oas.idschema('SecurityFindingId')
COMPLETION = 'enum:complete|stopped_budget|stopped_turns|timeout|failed|incomplete|invalid_output'
oas.schema('SecurityScanConfig', {'model': 's', 'budget_mode': 'enum:capped|unlimited', 'budget_usd': 'num', 'max_turns': 'i32'})
oas.schema('SecurityScanCounts', {'by_severity': 'obj', 'blocking': 'i32'})
oas.schema('SecurityScanDto', {'id': '$SecurityScanId', 'project_id': 's', 'run_id': 's', 'head_sha': 's', 'target_kind': 'enum:local_clone', 'tool_version': '?s', 'config': '$SecurityScanConfig',
  'state': 'enum:queued|running|finished|cancelled', 'completion': '?%s' % COMPLETION, 'tree_modified': 'b', 'tree_diff_artifact_id': '?s', 'report_artifact_id': '?s', 'stdout_artifact_id': '?s',
  'counts': '$SecurityScanCounts', 'started_at': '?s', 'finished_at': '?s', 'created_at': 's', 'revision': 'i64'})
oas.page('SecurityScanDto')
oas.schema('SecurityFindingDto', {'id': '$SecurityFindingId', 'source': 'enum:security', 'scan_id': '$SecurityScanId', 'run_id': 's', 'head_sha': 's', 'finding_key': 's',
  'severity': 'enum:blocker|major|minor|nit', 'severity_raw': 'enum:critical|high|medium|low|info', 'blocking': 'b', 'file': 's', 'line': 'i32', 'location_valid': 'b', 'cwe': '?s', 'title': 's', 'body': 's',
  'proof_artifact_id': '?s', 'status': 'enum:open|waived|superseded', 'waiver': '?obj', 'created_at': 's', 'revision': 'i64'})
oas.schema('SecurityFindingList', {'items': '[$SecurityFindingDto]'})
oas.schema('SecurityStatusDto', {'usable': 'obj', 'policy': 'obj', 'counts': 'obj'})
oas.schema('StartSecurityScanRequest', {'target_kind': 'enum:local_clone'})
oas.schema('WaiveSecurityFindingRequest', {'text': 's'})
oas.schema('SecurityIssueDraft', {'markdown': 's'})
N = ('scnId', 'SecurityScanId')
F = ('sfdId', 'SecurityFindingId')
oas.op('get', '/security/status', 'get_security_status', 'security', resp='$SecurityStatusDto')
oas.op('get', '/security/scans', 'list_security_scans', 'security', resp='$Page_SecurityScanDto', query=[('project_id', 's', ''), ('run_id', 's', '')])
oas.op('post', '/runs/{runId}/security-scans', 'start_security_scan', 'security', params=[('runId', 'RunId')], idem=True, body='StartSecurityScanRequest', ok=202, resp='$OperationDto')
oas.op('get', '/security/scans/{scnId}', 'get_security_scan', 'security', params=[N], resp='$SecurityScanDto')
oas.op('post', '/security/scans/{scnId}/cancel', 'cancel_security_scan', 'security', params=[N], ifmatch=True, resp='$SecurityScanDto')
oas.op('get', '/security/scans/{scnId}/findings', 'list_scan_findings', 'security', params=[N], resp='$SecurityFindingList')
oas.op('get', '/runs/{runId}/security-findings', 'list_run_security_findings', 'security', params=[('runId', 'RunId')], resp='$SecurityFindingList')
oas.op('post', '/security-findings/{sfdId}/waive', 'waive_security_finding', 'security', params=[F], ifmatch=True, body='WaiveSecurityFindingRequest', resp='$SecurityFindingDto')
oas.op('get', '/security-findings/{sfdId}/issue-draft', 'get_security_issue_draft', 'security', params=[F], resp='$SecurityIssueDraft')
oas.save()
