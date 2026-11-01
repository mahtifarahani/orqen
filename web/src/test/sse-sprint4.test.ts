import { QueryClient } from '@tanstack/react-query'
import { describe, expect, it } from 'vitest'
import { invalidateFor } from '@/api/sse'
import type { EventDto } from '@/api/generated/model'

const ORG = 'org_A'
const base = `/api/v1/orgs/${ORG}`
const paths = ['tasks', 'tasks/summary', 'runs/run_1', 'runs/run_1/roles', 'merge-requests', 'merge-requests/mrq_1', 'external-writes', 'host', 'connections/con_1/write-credential', 'inbox', 'rules']
const ev = (type: string): EventDto => ({ seq: 1, type, actor: 'system', subject: { kind: 'run', id: 'run_1' }, payload: {}, created_at: '2026-01-01T00:00:00Z' }) as EventDto

function invalidated(type: string): string[] {
  const qc = new QueryClient()
  for (const p of paths) qc.setQueryData([`${base}/${p}`], {})
  invalidateFor(qc, ORG, ev(type))
  return paths.filter((p) => qc.getQueryState([`${base}/${p}`])?.isInvalidated)
}

describe('Sprint 4 event invalidation', () => {
  it('role, verification, review and merge request events refresh runs, tasks and merge requests', () => {
    for (const t of ['role.dispatched', 'verification.recorded', 'review.recorded', 'mr.ready', 'extwrite.succeeded', 'finding.waived', 'coding_slot.acquired']) {
      expect(invalidated(t)).toEqual(expect.arrayContaining(['runs/run_1', 'runs/run_1/roles', 'tasks', 'merge-requests/mrq_1']))
    }
  })
  it('write settings and credential events refresh the settings and the credential', () => {
    expect(invalidated('external_writes.mode_changed')).toContain('external-writes')
    expect(invalidated('write_credential.set')).toContain('connections/con_1/write-credential')
  })
  it('host events refresh the host status', () => {
    expect(invalidated('host.probed')).toContain('host')
    expect(invalidated('host.spike_attested')).toContain('host')
  })
  it('unrelated events leave them alone', () => {
    expect(invalidated('pinned_rule.created')).toEqual(['rules'])
  })
})
