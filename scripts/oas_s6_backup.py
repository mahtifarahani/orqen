"""Sprint 6 WP103: backup, prune, verify, last restore and support bundle routes. Run: python3 scripts/oas_s6_backup.py"""
import oas
oas.load()
oas.idschema('BackupName')
oas.doc['components']['schemas']['BackupName'] = {'type': 'string', 'pattern': r'^orqen-backup-\d{8}T\d{6}Z-[0-9a-f]{8}$'}
oas.schema('BackupOrg', {'id': 's', 'slug': 's', 'name': 's'})
oas.schema('BackupSettingsDto', {'enabled': 'b', 'interval_hours': 'i32', 'keep_last': 'i32', 'keep_days': 'i32', 'dir': 's', 'default_dir': 's',
  'on_artifact_pattern': 'enum:fail|exclude_artifact', 'revision': 'i64'})
oas.schema('PutBackupSettingsRequest', {'enabled': 'b', 'interval_hours': 'i32', 'keep_last': 'i32', 'keep_days': 'i32', 'dir': 's', 'on_artifact_pattern': 'enum:fail|exclude_artifact'})
oas.schema('BackupEntryDto', {'name': '$BackupName', 'created_at': '?s', 'bytes': 'i64', 'orgs': '[$BackupOrg]', 'valid': 'b'})
oas.schema('BackupFailureDto', {'code': 's', 'location': '?s'})
oas.schema('BackupRunDto', {'id': 's', 'name': 's', 'state': 'enum:running|succeeded|failed', 'trigger': 'enum:manual|schedule', 'bytes': 'i64', 'failure': '?$BackupFailureDto', 'started_at': 's', 'finished_at': '?s'})
oas.schema('BackupListDto', {'dir': 's', 'entries': '[$BackupEntryDto]', 'runs': '[$BackupRunDto]', 'next_due_at': '?s'})
oas.schema('BackupCollectionEntry', {'name': 's', 'docs': 'i64', 'file': 's', 'sha256': 's', 'bytes': 'i64'})
oas.schema('BackupManifestDto', {'name': '$BackupName', 'bytes': 'i64', 'format': 's', 'format_version': 'i32', 'orqen_version': 's', 'schema_version': 'i32', 'policy_defaults_version': 'i32',
  'created_at': 's', 'cluster_time': 's', 'orgs': '[$BackupOrg]', 'collections': '[$BackupCollectionEntry]', 'artifacts': 'obj', 'excluded': 'obj', 'secret_scan': 'obj'})
oas.schema('RestoreFileResult', {'file': 's', 'ok': 'b', 'docs': 'i64'}, ['file', 'ok'])
oas.schema('RestoreCredentialRef', {'ref': 's', 'kind': 'enum:connection|publisher_credential|catherd_secret|extension', 'owner': 's', 'org_id': 's', 'present': 'b'})
oas.schema('RestoreProfileRef', {'org_id': 's', 'name': 's', 'present': 'b'})
oas.schema('RestorePreviewDto', {'name': 's', 'integrity': '[$RestoreFileResult]', 'integrity_ok': 'b', 'first_failed_file': 's', 'schema_version': 'i32', 'schema_compatible': 'b', 'target_empty': 'b',
  'target_non_empty': '[s]', 'counts': 'obj', 'orgs': '[$BackupOrg]', 'missing_credentials': '[$RestoreCredentialRef]', 'held_runs': 'i32', 'republish_profiles': '[$RestoreProfileRef]',
  'can_restore': 'b', 'notes': '[s]'}, ['name', 'integrity', 'integrity_ok', 'schema_version', 'schema_compatible', 'target_empty', 'target_non_empty', 'counts', 'orgs', 'missing_credentials', 'held_runs', 'republish_profiles', 'can_restore', 'notes'])
oas.schema('BackupPrunePreview', {'delete': '[s]', 'keep': '[s]'})
oas.schema('PruneBackupsRequest', {'confirm': 'b'})
oas.schema('PruneBackupsResult', {'deleted': '[s]'})
oas.schema('RestoreReportDto', {'id': '?s'}, [])
oas.schema('SupportBundleDto', {'generated_at': 's', 'versions': 'obj', 'preflight': '[obj]', 'health': '[obj]', 'extensions': '[obj]', 'job_failures_7d': '[obj]', 'restore': '?obj', 'backup': 'obj'})
B = ('backupName', 'BackupName')
oas.op('get', '/system/backups/settings', 'get_backup_settings', 'system', resp='$BackupSettingsDto')
oas.op('put', '/system/backups/settings', 'put_backup_settings', 'system', ifmatch=True, body='PutBackupSettingsRequest', resp='$BackupSettingsDto')
oas.op('get', '/system/backups', 'list_backups', 'system', resp='$BackupListDto')
oas.op('post', '/system/backups', 'start_backup', 'system', idem=True, ok=202, resp='$OperationDto')
oas.op('get', '/system/backups/prune-preview', 'preview_backup_prune', 'system', resp='$BackupPrunePreview')
oas.op('post', '/system/backups/prune', 'prune_backups', 'system', idem=True, body='PruneBackupsRequest', resp='$PruneBackupsResult')
oas.op('get', '/system/backups/{backupName}', 'get_backup', 'system', params=[B], resp='$BackupManifestDto')
oas.op('post', '/system/backups/{backupName}/verify', 'verify_backup', 'system', params=[B], idem=True, resp='$RestorePreviewDto')
oas.op('get', '/system/restore/last', 'get_last_restore', 'system', resp='$RestoreReportDto')
oas.op('get', '/system/support-bundle', 'get_support_bundle', 'system', resp='$SupportBundleDto')
oas.save()
