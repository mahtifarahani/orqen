import { QueryClient } from '@tanstack/react-query'
import { describe, expect, it } from 'vitest'
import { invalidateFor } from '@/api/sse'
import type { EventDto } from '@/api/generated/model'

const ORG = 'org_A'
const base = `/api/v1/orgs/${ORG}`
const paths = ['knowledge', 'knowledge-author-sessions', 'knowledge-proposals', 'tasks', 'tasks/summary', 'code-indexes', 'graph/status', 'security/scans', 'rules']
const ev = (type: string): EventDto => ({ seq: 1, type, actor: 'system', subject: { kind: 'x', id: 'x' }, payload: {}, created_at: '2026-01-01T00:00:00Z' }) as EventDto

function invalidated(type: string): string[] {
  const qc = new QueryClient()
  for (const p of paths) qc.setQueryData([`${base}/${p}`], {})
  invalidateFor(qc, ORG, ev(type))
  return paths.filter((p) => qc.getQueryState([`${base}/${p}`])?.isInvalidated)
}

describe('Sprint 7 event invalidation', () => {
  it('author session events refresh sessions, proposals and records only', () => {
    const got = invalidated('knowledge.author_session')
    expect(got).toEqual(expect.arrayContaining(['knowledge', 'knowledge-author-sessions', 'knowledge-proposals']))
    expect(got).not.toContain('rules')
    expect(got).not.toContain('tasks')
  })
  it('security events also refresh the tasks list and summary', () => {
    expect(invalidated('security.scan_auto_queued')).toEqual(expect.arrayContaining(['tasks', 'tasks/summary', 'security/scans']))
  })
  it('the automatic code index event refreshes the code index queries', () => {
    expect(invalidated('code_index.auto_queued')).toEqual(expect.arrayContaining(['code-indexes', 'graph/status']))
  })
})
