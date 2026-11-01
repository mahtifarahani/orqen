"""Sprint 5 WP84: applies_to_roles on pinned rules (optional, empty = all roles). Run: python3 scripts/oas_s5_rules.py"""
import oas
oas.load()
for n in ('PinnedRuleDto', 'CreatePinnedRuleRequest', 'PatchPinnedRuleRequest'):
    props = oas.doc['components']['schemas'][n]['properties']
    props['applies_to_roles'] = {'type': ['array', 'null'], 'items': {'type': 'string', 'enum': ['architect', 'worker', 'verifier', 'reviewer', 'writer']}} if n != 'PinnedRuleDto' else {'type': 'array', 'items': {'type': 'string', 'enum': ['architect', 'worker', 'verifier', 'reviewer', 'writer']}}
oas.save()
