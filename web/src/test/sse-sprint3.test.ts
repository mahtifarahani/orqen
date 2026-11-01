import { QueryClient } from '@tanstack/react-query'
import { describe, expect, it } from 'vitest'
import { invalidateFor } from '@/api/sse'
import type { EventDto } from '@/api/generated/model'

const ORG = 'org_A'
const base = `/api/v1/orgs/${ORG}`
const paths = ['tasks', 'tasks/summary', 'runs/run_1', 'inbox', 'rules', 'automation', 'projects/prj_1/intake', 'projects/prj_1']
const ev = (type: string): EventDto => ({ seq: 1, type, actor: 'system', subject: { kind: 'run', id: 'run_1' }, payload: {}, created_at: '2026-01-01T00:00:00Z' }) as EventDto

function invalidated(type: string): string[] {
  const qc = new QueryClient()
  for (const p of paths) qc.setQueryData([`${base}/${p}`], {})
  invalidateFor(qc, ORG, ev(type))
  return paths.filter((p) => qc.getQueryState([`${base}/${p}`])?.isInvalidated)
}

describe('Sprint 3 event invalidation', () => {
  it('run, issue and intake events refresh tasks, runs and the intake status', () => {
    for (const t of ['run.state_changed', 'issue.ingested', 'intake.polled']) {
      expect(invalidated(t)).toEqual(expect.arrayContaining(['tasks', 'tasks/summary', 'runs/run_1', 'projects/prj_1/intake']))
    }
  })
  it('inbox events refresh the inbox, tasks and runs', () => {
    const got = invalidated('inbox.answered')
    expect(got).toEqual(expect.arrayContaining(['inbox', 'tasks', 'runs/run_1']))
    expect(got).not.toContain('rules')
  })
  it('workspace, context and decision events refresh runs', () => {
    for (const t of ['workspace.ready', 'context.packet_built', 'decision.recorded']) expect(invalidated(t)).toContain('runs/run_1')
  })
  it('automation and rule events refresh their own queries', () => {
    expect(invalidated('automation.paused')).toEqual(expect.arrayContaining(['automation', 'tasks/summary']))
    expect(invalidated('pinned_rule.created')).toEqual(['rules'])
  })
})
