"""Sprint 5 WP82: role context manifest. Run: python3 scripts/oas_s5_context.py"""
import oas
oas.load()
oas.doc['components']['schemas']['RoleName'] = {'type': 'string', 'enum': ['architect', 'worker', 'verifier', 'reviewer', 'writer']}
oas.schema('RoleContext', {'role': 's', 'manifest': 'obj', 'oversize': '?obj'})
oas.op('get', '/runs/{runId}/context/roles/{role}', 'get_role_context', 'runs', params=[('runId', 'RunId'), ('role', 'RoleName')], resp='$RoleContext')
oas.save()
