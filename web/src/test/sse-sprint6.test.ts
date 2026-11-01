import { QueryClient } from '@tanstack/react-query'
import { describe, expect, it } from 'vitest'
import { invalidateFor } from '@/api/sse'
import type { EventDto } from '@/api/generated/model'

const ORG = 'org_A'
const base = `/api/v1/orgs/${ORG}`
const paths = ['extensions', 'extensions/openclaw-wiki', 'skills', 'skill-bindings', 'security/status', 'security/scans', 'code-indexes', 'graph/status', 'system/backups', 'system/restore/last', 'rules']
const ev = (type: string): EventDto => ({ seq: 1, type, actor: 'system', subject: { kind: 'x', id: 'x' }, payload: {}, created_at: '2026-01-01T00:00:00Z' }) as EventDto

function invalidated(type: string): string[] {
  const qc = new QueryClient()
  for (const p of paths) qc.setQueryData([`${base}/${p}`], {})
  invalidateFor(qc, ORG, ev(type))
  return paths.filter((p) => qc.getQueryState([`${base}/${p}`])?.isInvalidated)
}

describe('Sprint 6 event invalidation', () => {
  it('extension events refresh the extensions and the dependent statuses', () => {
    expect(invalidated('extension.enabled')).toEqual(expect.arrayContaining(['extensions', 'extensions/openclaw-wiki', 'security/status', 'graph/status']))
  })
  it('skill events refresh skills and bindings', () => {
    expect(invalidated('skill.approved')).toEqual(expect.arrayContaining(['skills', 'skill-bindings']))
    expect(invalidated('skills.bindings_changed')).toEqual(expect.arrayContaining(['skill-bindings']))
  })
  it('security and code index events refresh their lists', () => {
    expect(invalidated('security.scan_finished')).toEqual(expect.arrayContaining(['security/status', 'security/scans']))
    expect(invalidated('code_index.ready')).toEqual(expect.arrayContaining(['code-indexes', 'graph/status']))
  })
  it('backup events refresh the system pages only', () => {
    expect(invalidated('backup.finished')).toEqual(expect.arrayContaining(['system/backups']))
    expect(invalidated('backup.finished')).not.toContain('rules')
  })
})
