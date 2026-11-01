"""Sprint 6 WP99: extension routes. Run: python3 scripts/oas_s6_extensions.py"""
import oas
oas.load()
oas.idschema('ExtensionId')
oas.idschema('ExtensionInstallId')
oas.schema('ExtensionLicenseInfo', {'spdx': 's', 'gate': 'enum:none|acceptance_required', 'notice_key': 's'})
oas.schema('ExtensionCredentialField', {'field': 's', 'label_key': 's', 'required': 'b'})
oas.schema('ExtensionManifestDto', {'id': '$ExtensionId', 'version': 's', 'source': 's', 'kind': 'enum:knowledge_index|code_graph|security_scan|mcp_connector|design', 'integration': 'enum:integrated|detect_only',
  'transport': 'enum:cli|mcp_stdio|remote_mcp', 'license': '$ExtensionLicenseInfo', 'runtime': 'obj', 'config_schema': '?obj', 'credential_fields': '[$ExtensionCredentialField]',
  'capabilities': '[s]', 'read_tools': '[s]', 'write_tools': '[s]', 'supported_hosts': '[s]', 'requires': 'obj'})
oas.schema('ExtensionUsable', {'ok': 'b', 'reason': '?s'})
oas.schema('ExtensionHealthDto', {'status': 'enum:ok|changed|missing|failed', 'code': '?s', 'at': 's'})
oas.schema('ExtensionSummaryDto', {'manifest': '$ExtensionManifestDto', 'state': 'enum:absent|registered|license_required|detected|detect_failed|tested|test_failed|enabled|disabled|changed',
  'usable': '$ExtensionUsable', 'health': '?$ExtensionHealthDto', 'revision': 'i64'})
oas.schema('ExtensionList', {'items': '[$ExtensionSummaryDto]'})
oas.schema('ExtensionInstallDto', {'id': '$ExtensionInstallId', 'number': 'i32', 'path': 's', 'version': 's', 'sha256': 's', 'capability_report': 'obj', 'tested_at': 's', 'active': 'b'})
oas.schema('ExtensionCredentialState', {'field': 's', 'set': 'b'})
oas.schema('ExtensionBindingState', {'project_id': 's', 'enabled': 'b', 'revision': 'i64'})
oas.schema('ExtensionLicenseState', {'license_id': 's', 'notice_sha256': 's', 'basis': 's', 'note': 's', 'accepted_at': 's', 'revoked_at': '?s'})
oas.schema('ExtensionNotice', {'locale': 'enum:en|fa', 'text': 's', 'sha256': 's'})
oas.schema('ExtensionDetected', {'version': 's', 'sha256': 's', 'at': 's'})
oas.schema('ExtensionLastTest', {'ok': 'b', 'code': '?s', 'report': '?obj', 'at': 's'})
oas.schema('ExtensionInstallGuidance', {'text_key': 's', 'commands': '[s]'})
oas.schema('ExtensionDetailDto', {'manifest': '$ExtensionManifestDto', 'state': 's', 'usable': '$ExtensionUsable', 'health': '?$ExtensionHealthDto', 'revision': 'i64',
  'path': '?s', 'detected': '?$ExtensionDetected', 'last_test': '?$ExtensionLastTest', 'registered_at': '?s', 'last_error': '?s', 'disabled_reason': '?s',
  'license_state': '?$ExtensionLicenseState', 'license_notices': '[$ExtensionNotice]', 'config': 'obj', 'installs': '[$ExtensionInstallDto]',
  'credentials': '[$ExtensionCredentialState]', 'project_bindings': '[$ExtensionBindingState]', 'install_guidance': '$ExtensionInstallGuidance'})
oas.schema('RegisterExtensionBinaryRequest', {'path': 's'})
oas.schema('ExtensionCandidates', {'paths': '[s]'})
oas.schema('AcceptExtensionLicenseRequest', {'basis': 'enum:noncommercial_use|commercial_license_obtained|other_compatible', 'note': 's', 'confirm_phrase': 's', 'notice_sha256': 's'})
oas.schema('SetExtensionConfigRequest', {'config': 'obj'})
oas.schema('SetExtensionCredentialRequest', {'value': 's'})
oas.schema('RollbackExtensionRequest', {'install_id': '$ExtensionInstallId'})
oas.schema('BindProjectExtensionRequest', {'enabled': 'b'})
oas.schema('ExtensionBindingDto', {'project_id': 's', 'extension_id': '$ExtensionId', 'enabled': 'b', 'revision': 'i64'})
E = ('extId', 'ExtensionId')
D = '$ExtensionDetailDto'
oas.op('get', '/extensions', 'list_extensions', 'extensions', resp='$ExtensionList')
oas.op('get', '/extensions/{extId}', 'get_extension', 'extensions', params=[E], resp=D)
oas.op('put', '/extensions/{extId}/binary', 'register_extension_binary', 'extensions', params=[E], ifmatch=True, body='RegisterExtensionBinaryRequest', resp=D)
oas.op('get', '/extensions/{extId}/candidates', 'find_extension_candidates', 'extensions', params=[E], resp='$ExtensionCandidates')
oas.op('post', '/extensions/{extId}/detect', 'detect_extension', 'extensions', params=[E], idem=True, ok=202, resp='$OperationDto')
oas.op('put', '/extensions/{extId}/license', 'accept_extension_license', 'extensions', params=[E], ifmatch=True, body='AcceptExtensionLicenseRequest', resp=D)
oas.op('delete', '/extensions/{extId}/license', 'revoke_extension_license', 'extensions', params=[E], ifmatch=True, resp=D)
oas.op('put', '/extensions/{extId}/config', 'set_extension_config', 'extensions', params=[E], ifmatch=True, body='SetExtensionConfigRequest', resp=D)
oas.op('put', '/extensions/{extId}/credentials/{field}', 'set_extension_credential', 'extensions', params=[E], body='SetExtensionCredentialRequest', resp='$ExtensionCredentialState')
oas.op('delete', '/extensions/{extId}/credentials/{field}', 'delete_extension_credential', 'extensions', params=[E], resp='$ExtensionCredentialState')
oas.op('post', '/extensions/{extId}/test', 'test_extension', 'extensions', params=[E], idem=True, ok=202, resp='$OperationDto')
oas.op('post', '/extensions/{extId}/enable', 'enable_extension', 'extensions', params=[E], ifmatch=True, resp=D)
oas.op('post', '/extensions/{extId}/disable', 'disable_extension', 'extensions', params=[E], ifmatch=True, resp=D)
oas.op('post', '/extensions/{extId}/rollback', 'rollback_extension', 'extensions', params=[E], ifmatch=True, body='RollbackExtensionRequest', resp=D)
oas.op('post', '/extensions/{extId}/health', 'check_extension_health', 'extensions', params=[E], idem=True, ok=202, resp='$OperationDto')
oas.op('put', '/projects/{prjId}/extensions/{extId}', 'bind_project_extension', 'extensions', params=[('prjId', 'ProjectId'), E], ifmatch=True, body='BindProjectExtensionRequest', resp='$ExtensionBindingDto')
# the {field} path parameter is a plain string
for method, op_id in (('put', 'set_extension_credential'), ('delete', 'delete_extension_credential')):
    o = oas.doc['paths']['/api/v1/orgs/{orgId}/extensions/{extId}/credentials/{field}'][method]
    o['parameters'].insert(2, {'name': 'field', 'in': 'path', 'required': True, 'schema': {'type': 'string'}})
oas.save()
