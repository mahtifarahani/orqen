"""Sprint 7 WP114: trigger on code indexes and security scans. Run: python3 scripts/oas_s7_sweeps.py"""
import oas
oas.load()
oas.add_props('CodeIndexDto', {'trigger': 'enum:manual|auto'})
oas.add_props('SecurityScanDto', {'trigger': 'enum:manual|auto'})
oas.save()
