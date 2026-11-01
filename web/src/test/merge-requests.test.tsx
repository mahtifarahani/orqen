import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { MergeRequestsPage } from '@/features/merge-requests/merge-requests-page'
import { CodingSlotCard, MergeRequestsCard, PublishingCard } from '@/features/overview/publish-cards'
import { applyLocale } from '@/i18n/direction'
import { NOW, ORG, renderRoute, stubApi, type Call } from './helpers'

const base = `/api/v1/orgs/${ORG}`
const mr = (id: string, mode: string, ready: boolean) => ({
  id, run_id: 'r', project_id: 'p', mode, iid: null, web_url: null, title: `ORQ-${id}`, source_branch: `orqen/${id}`, target_branch: 'main', draft: true, state: 'opened',
  current_head: 'h1', last_pushed_sha: '', verified_head: 'h1', reviewed_head: 'h1', pipeline: null, discussions_resolved: true, has_conflicts: false,
  readiness: { ready, evaluated_at: NOW, head: 'h1', checks: [{ code: 'remote_mr', status: ready ? 'pass' : 'fail', detail: '' }] }, merged: null, created_at: NOW, updated_at: NOW, revision: 1,
})
const api = (c: Call) => {
  if (c.path === `${base}/merge-requests` && c.method === 'GET') {
    const items = [mr('a', 'dry_run', false), mr('b', 'live', true)].filter((m) => (c.search.get('mode') ? m.mode === c.search.get('mode') : true) && (c.search.get('ready') ? m.readiness.ready : true))
    return { body: { items, next_cursor: null } }
  }
  if (c.path === `${base}/merge-requests/b`) return { body: mr('b', 'live', true) }
  if (c.path === `${base}/merge-requests/b/refresh`) return { status: 202, body: { id: 'op', status: 'pending' } }
  if (c.path === `${base}/tasks/summary`)
    return { body: { runs_by_state: {}, inbox_open: 0, inbox_open_by_reason: {}, candidates: 0, intake: [], decisions_24h: {}, workspaces_by_state: {}, evaluated_at: NOW, merge_requests: { ready: 2, awaiting_ci: 1, dry_run: 3 }, external_operations: { awaiting_approval: 1, outcome_unknown: 0 }, coding_slot: { run_id: 'run_9', queued: 2 } } }
  if (c.path === `${base}/external-writes`) return { body: { mode: 'dry_run', allowlist: [], paused: false, paused_reason: null, env_guard: { build_channel: 'dev', orqen_env: '', scope: 'loopback_only', kill_switch: false }, revision: 1, mode_changed_at: null } }
  return undefined
}
beforeEach(() => applyLocale('en'))
afterEach(() => vi.unstubAllGlobals())

describe('merge requests', () => {
  it('lists simulated and real MRs, filters by mode and opens the sheet with a refresh for live ones', async () => {
    const calls = stubApi(api)
    renderRoute(<MergeRequestsPage />, `/orgs/${ORG}/merge-requests`, '/orgs/:orgId/merge-requests')
    expect(await screen.findByText('ORQ-a')).toBeInTheDocument()
    expect(screen.getByText('ORQ-b')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('switch', { name: 'Ready only' }))
    await waitFor(() => expect(screen.queryByText('ORQ-a')).not.toBeInTheDocument())
    expect(calls.some((c) => c.search.get('ready') === 'true')).toBe(true)
    await userEvent.click(screen.getByRole('button', { name: /ORQ-b/ }))
    const sheet = await screen.findByRole('dialog')
    await userEvent.click(await within(sheet).findByRole('button', { name: 'Refresh from GitLab' }))
    await waitFor(() => expect(calls.some((c) => c.path.endsWith('/refresh'))).toBe(true))
    expect(calls.find((c) => c.path.endsWith('/refresh'))?.headers.get('Idempotency-Key')).toBeTruthy()
  })
  it('the overview cards show counts, the write mode and the slot holder', async () => {
    stubApi(api)
    renderRoute(
      <>
        <MergeRequestsCard orgId={ORG} />
        <PublishingCard orgId={ORG} />
        <CodingSlotCard orgId={ORG} />
      </>,
      `/orgs/${ORG}/overview`,
      '/orgs/:orgId/overview',
    )
    expect(await screen.findByText('Ready to merge')).toBeInTheDocument()
    expect(await screen.findByText('DRY-RUN')).toBeInTheDocument()
    expect(await screen.findByText('run_9')).toBeInTheDocument()
  })
})
