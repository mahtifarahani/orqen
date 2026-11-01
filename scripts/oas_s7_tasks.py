"""Sprint 7 WP115: security summary on tasks. Run: python3 scripts/oas_s7_tasks.py"""
import oas
oas.load()
oas.schema('TaskSecurityDto', {'scan_id': 's', 'head_sha': 's', 'state': 'enum:queued|running|finished|cancelled', 'completion': '?s', 'open_blocking': 'i64', 'stale': 'b'})
oas.schema('TaskSecurityCounts', {'open_blocking': 'i64', 'scans_running': 'i64'})
oas.add_props('TaskSummaryDto', {'security': '?$TaskSecurityDto'})
oas.add_props('TaskSummaryCountsDto', {'security': '$TaskSecurityCounts'})
d = oas.doc['components']['schemas']
if 'TaskDto' in d:
    props = d['TaskDto'].get('properties', {})
    if 'security' not in props and 'allOf' not in d['TaskDto']:
        oas.add_props('TaskDto', {'security': '?$TaskSecurityDto'})
oas.save()
