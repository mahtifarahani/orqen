"""Sprint 6 WP104: restore report and republish flag. Run after oas_s6_backup.py: python3 scripts/oas_s6_restore.py"""
import oas
oas.load()
oas.schema('RestoreMissingRef', {'org_id': 's', 'kind': 's', 'owner': 's', 'ref': 's'})
oas.schema('RestoreRepublish', {'org_id': 's', 'name': 's'})
oas.schema('RestorePostCheck', {'at': 's', 'missing_credentials': '[obj]', 'republish_required': '[obj]'})
oas.schema('RestoreReportDto', {'id': 's', 'backup_name': 's', 'created_at': 's', 'orgs': '[s]', 'counts': 'obj', 'blobs': 'i32', 'held_runs': 'i64',
  'missing_credentials': '[$RestoreMissingRef]', 'republish_profiles': '[$RestoreRepublish]', 'post_check': 'obj'})
oas.add_props('ModelProfileVersionDto', {'republish_required': 'b'})
oas.save()
