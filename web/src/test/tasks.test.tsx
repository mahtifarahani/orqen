import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { RunStateBadge, RUN_STATES } from '@/components/run-state-badge'
import { TaskDetailPage } from '@/features/tasks/task-detail-page'
import { TasksPage } from '@/features/tasks/tasks-page'
import { applyLocale } from '@/i18n/direction'
import en from '@/i18n/en.json'
import { NOW, ORG, problem, renderRoute, stubApi, type Call } from './helpers'

const base = `/api/v1/orgs/${ORG}`
const runSummary = (over: Record<string, unknown> = {}) => ({ id: 'run_1', state: 'planned', stage: 'start_gate', hold: { code: 'coding_start', detail: '' }, eligibility: 'eligible', stale: false, open_inbox_id: null, risk: 'low', queue_position: null, attempt: 1, updated_at: NOW, revision: 7, ...over })
const task = (over: Record<string, unknown> = {}) => ({
  id: 'iss_1', project_id: 'prj_1', connection_id: 'con_1', issue_id: '1', issue_key: 'ORQ-1', issue_url: 'http://jira/browse/ORQ-1', summary: 'Export CSV', priority: 'High', status_name: 'To Do', in_filter: true, candidate: false, inaccessible: false, ambiguous_project: [], last_seen_at: NOW, run: runSummary(), ...over,
})
const snapshot = { id: 'isn_1', revision: 1, fetched_at: NOW, jira_updated: NOW, summary: 'Export CSV', description: 'Users need it', description_truncated: false, acceptance: { text: '* a', source: 'section', items: ['The button downloads a CSV'] }, issue_type: 'Task', status: { name: 'To Do', category: 'new' }, priority: 'High', labels: [], components: [], parent: null, links: [], comments: [], attachments: [], refs: { confluence: [], figma: [] }, material_fingerprint: 'x' }
const runDto = (over: Record<string, unknown> = {}) => ({
  ...runSummary(), project_id: 'prj_1', connection_id: 'con_1', issue_id: '1', issue_key: 'ORQ-1', tracked_issue_id: 'iss_1', origin: 'auto_intake', active: true, priority: 'high', snapshot_id: 'isn_1', snapshot_revision: 1, sources: [{ id: 'src_1', kind: 'jira_issue', label: 'ORQ-1', ref: 'isn_1', content_hash: 'h', text: null, inbox_id: null, revision_added: 1, carried: false, code: '' }],
  sources_revision: 1, pinned: { policy_version_id: 'pol_1', profile_version_id: null, host: 'codex', packet_cap: 16384 }, context: { artifact_id: 'art_p', manifest_artifact_id: 'art_m', hash: 'h', bytes: 900, cap: 16384, sources_revision: 1, built_at: NOW },
  triage: { outcome: 'execute', reasons: [], risk: 'low', risk_reasons: [], source: 'fixed_rules', decision_id: null, sources_revision: 1, decided_at: NOW }, plan: null, repo_head_sha: null, workspace_id: null, queued_at: null, cancellation: null, terminal: null, failure: null,
  supersedes: null, superseded_by: null, intent_key: 'i', material_fingerprint: 'f', gates: { plan_approval: 'manual', coding_start: 'manual' }, attempts: [{ number: 1, reason: 'initial', from_stage: 'context', started_at: NOW, ended_at: null, outcome: '' }], created_at: NOW, workspace: null, ...over,
})
const manifest = { version: 1, cap: 16384, bytes: 900, critical_bytes: 300, sources_revision: 1, built_at: NOW, packet_artifact_id: 'art_p', freshness: { issue_snapshot_revision: 1, issue_fetched_at: NOW, repo_head_sha: 'abcdef0123456789', contract_snapshot_id: null },
  sections: [{ id: 'goal_acceptance', bytes: 500, items: [{ source_id: 's1', kind: 'jira_summary', label: 'ORQ-1 summary', ref: '', bytes: 500, included: true, truncated: false, original_bytes: 500 }] }, { id: 'design_refs', bytes: 0, items: [{ source_id: 's2', kind: 'figma', label: 'https://figma.com/x', ref: '', bytes: 0, included: false, truncated: false, original_bytes: 0, reason: 'adapter_unavailable' }] }],
  excluded: [{ source_id: 's2', kind: 'figma', label: 'https://figma.com/x', reason: 'adapter_unavailable' }] }

let startResult: { status?: number; body?: unknown }
let run = runDto()
const api = (c: Call) => {
  if (c.path === `${base}/tasks` && c.method === 'GET') return { body: { items: [task(), task({ id: 'iss_2', issue_key: 'ORQ-2', summary: 'Second issue', candidate: true, run: null })], next_cursor: null } }
  if (c.path === `${base}/projects`) return { body: { items: [{ id: 'prj_1', name: 'App', primary_repo: null, state: 'ready', blocker_count: 0, automation_enabled: true, revision: 1 }], next_cursor: null } }
  if (c.path === `${base}/tasks/iss_1`) return { body: { ...task(), snapshot, runs: [runSummary()] } }
  if (c.path === `${base}/runs/run_1` && c.method === 'GET') return { body: run }
  if (c.path === `${base}/runs/run_1/context`) return { body: manifest }
  if (c.path === `${base}/runs/run_1/start`) return startResult
  if (c.path === `${base}/tasks/iss_2/evaluate`) return { status: 201, body: runDto({ id: 'run_2' }) }
  return undefined
}
beforeEach(() => {
  applyLocale('en')
  run = runDto()
  startResult = { body: runDto({ hold: null }) }
})
afterEach(() => {
  vi.unstubAllGlobals()
  applyLocale('en')
})

describe('run state badges', () => {
  it('render a word for every one of the 19 states', () => {
    const states = Object.keys(RUN_STATES)
    expect(states).toHaveLength(19)
    for (const s of states) {
      const { unmount } = render(<RunStateBadge state={s} />)
      expect(screen.getByText((en.run.state as Record<string, string>)[s])).toBeInTheDocument()
      unmount()
    }
  })
})

describe('tasks page', () => {
  it('lists tasks, searches by key and evaluates a candidate with one key per intent', async () => {
    const calls = stubApi(api)
    renderRoute(<TasksPage />, `/orgs/${ORG}/tasks`, '/orgs/:orgId/tasks')
    expect(await screen.findByText('Export CSV')).toBeInTheDocument()
    expect(screen.getByText('Waiting for Start')).toBeInTheDocument()
    await userEvent.type(screen.getByLabelText('Issue key'), 'ORQ')
    await waitFor(() => expect(calls.some((c) => c.path === `${base}/tasks` && c.search.get('q') === 'ORQ')).toBe(true))
    await userEvent.click(await screen.findByRole('button', { name: 'Evaluate ORQ-2' }))
    await waitFor(() => expect(calls.filter((c) => c.path.endsWith('/evaluate'))).toHaveLength(1))
    await userEvent.click(screen.getByRole('button', { name: 'Evaluate ORQ-2' }))
    await waitFor(() => expect(calls.filter((c) => c.path.endsWith('/evaluate'))).toHaveLength(2))
    const [a, b] = calls.filter((c) => c.path.endsWith('/evaluate')).map((c) => c.headers.get('Idempotency-Key'))
    expect(a).toBeTruthy()
    expect(b).toBeTruthy()
    expect(a).not.toBe(b) // renewed after the first one succeeded
  })
  it('puts keys in LTR and Jira text on dir=auto; fa flows right to left', async () => {
    applyLocale('fa')
    stubApi(api)
    renderRoute(<TasksPage />, `/orgs/${ORG}/tasks`, '/orgs/:orgId/tasks')
    const summary = await screen.findByText('Export CSV')
    expect(summary).toHaveAttribute('dir', 'auto')
    const key = screen.getAllByText('ORQ-1')[0]
    expect(key.closest('bdi')).toHaveAttribute('dir', 'ltr')
    expect(document.documentElement.dir).toBe('rtl')
  })
})

describe('task detail', () => {
  const render = (search = '') => renderRoute(<TaskDetailPage />, `/orgs/${ORG}/tasks/iss_1${search}`, '/orgs/:orgId/tasks/:taskId')
  it('Start sends If-Match and an Idempotency-Key', async () => {
    const calls = stubApi(api)
    render()
    await userEvent.click(await screen.findByRole('button', { name: 'Start' }))
    await waitFor(() => expect(calls.some((c) => c.path.endsWith('/start'))).toBe(true))
    const post = calls.find((c) => c.path.endsWith('/start'))
    expect(post?.headers.get('If-Match')).toBe('"7"')
    expect(post?.headers.get('Idempotency-Key')).toBeTruthy()
  })
  it('disables Start and lists the blockers on 409 run_not_startable', async () => {
    startResult = problem(409, 'run_not_startable', { errors: [{ field: 'catherd_unavailable', message: 'x' }] })
    stubApi(api)
    render()
    const start = await screen.findByRole('button', { name: 'Start' })
    await userEvent.click(start)
    const alert = await screen.findByTestId('start-blockers')
    expect(within(alert).getByText('This run cannot start yet')).toBeInTheDocument()
    expect(within(alert).getAllByRole('listitem')).toHaveLength(1)
    expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled()
  })
  it('the context tab shows the cap meter and the exclusion reasons', async () => {
    stubApi(api)
    render('?tab=context')
    const meter = await screen.findByRole('meter')
    expect(meter).toHaveAttribute('aria-valuenow', '900')
    expect(meter).toHaveAttribute('aria-valuemax', '16384')
    expect(await screen.findByText('Adapter not available')).toBeInTheDocument()
    expect(screen.getByText('Design references')).toBeInTheDocument()
  })
  it('Cancel asks for confirmation and sends the reason', async () => {
    const calls = stubApi((c) => (c.path.endsWith('/cancel') ? { body: runDto({ active: false, state: 'cancelled', hold: null }) } : api(c)))
    render()
    await userEvent.click(await screen.findByRole('button', { name: 'Cancel run' }))
    await userEvent.type(screen.getByLabelText('Reason (optional)'), 'wrong ticket')
    await userEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Cancel run' }))
    await waitFor(() => expect(calls.some((c) => c.path.endsWith('/cancel'))).toBe(true))
    expect(calls.find((c) => c.path.endsWith('/cancel'))?.body).toEqual({ reason: 'wrong ticket' })
  })
})
