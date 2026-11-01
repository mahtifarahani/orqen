import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { KnowledgePage } from '@/features/knowledge/knowledge-page'
import { SecurityBadge } from '@/features/tasks/security-badge'
import { TasksPage } from '@/features/tasks/tasks-page'
import { applyLocale } from '@/i18n/direction'
import { NOW, ORG, renderRoute, stubApi, type Call } from './helpers'

const base = `/api/v1/orgs/${ORG}`
const session = (over: Record<string, unknown> = {}) => ({
  id: 'kas_1', run_id: 'run_1', attempt: 1, trigger: 'merge', state: 'failed', code: 'reply_unparseable', writer: { name: 'writer-K1', rung: null, status: null }, kverifier: { name: null, rung: null, status: null },
  proposal_id: null, fallback_proposal_id: null, created_at: NOW, updated_at: NOW, revision: 4, ...over,
})
let sessions: Record<string, unknown>[]
let proposals: Record<string, unknown>[]
const task = (security: unknown) => ({
  id: 'iss_1', project_id: 'prj_1', connection_id: 'con_1', issue_id: '1', issue_key: 'ORQ-1', issue_url: 'http://jira/browse/ORQ-1', summary: 'Export CSV', priority: 'High', status_name: 'To Do', in_filter: true, candidate: false, inaccessible: false,
  ambiguous_project: [], last_seen_at: NOW, run: null, security,
})
let tasks: Record<string, unknown>[]
const api = (c: Call) => {
  if (c.path === `${base}/knowledge-author-sessions` && c.method === 'GET') return { body: { items: sessions } }
  if (c.path === `${base}/knowledge-proposals`) return { body: { items: proposals, next_cursor: null } }
  if (c.path === `${base}/knowledge`) return { body: { items: [], next_cursor: null } }
  if (c.path === `${base}/knowledge-conflicts`) return { body: { items: [], next_cursor: null } }
  if (c.path === `${base}/wiki/status`) return { body: { configured: false, mode: 'fallback', degraded: null, pending: 0, applied_seq: 0, generation: 0, rebuild_requested: false } }
  if (c.path.endsWith('/retry')) return { status: 201, body: session({ id: 'kas_2', state: 'queued', attempt: 2, code: null }) }
  if (c.path.endsWith('/fallback')) return { body: { id: 'kpr_1', run_id: 'run_1', trigger: 'merge', bundle_artifact_id: null, state: 'awaiting_review', items: [], created_at: NOW } }
  if (c.path.endsWith('/cancel')) return { body: session({ state: 'cancelled' }) }
  if (c.path === `${base}/tasks`) return { body: { items: tasks, next_cursor: null } }
  if (c.path === `${base}/projects`) return { body: { items: [{ id: 'prj_1', name: 'App', primary_repo: null, state: 'ready', blocker_count: 0, automation_enabled: true, revision: 1 }], next_cursor: null } }
  return undefined
}
const page = () => renderRoute(<KnowledgePage />, `/orgs/${ORG}/knowledge`, '/orgs/:orgId/knowledge')

beforeEach(() => {
  applyLocale('en')
  sessions = [session()]
  proposals = []
  tasks = [task(null)]
})
afterEach(() => {
  vi.unstubAllGlobals()
  applyLocale('en')
})

describe('author sessions', () => {
  it('shows the translated state and code, and enables only the actions that fit the state', async () => {
    stubApi(api)
    page()
    const row = (await screen.findByText('Failed')).closest('li') as HTMLElement
    expect(within(row).getByText('The role answered in a form Orqen could not read')).toBeInTheDocument()
    expect(within(row).queryByText('reply_unparseable')).not.toBeInTheDocument()
    expect(within(row).getByRole('button', { name: 'Retry' })).toBeEnabled()
    expect(within(row).getByRole('button', { name: 'Use deterministic draft' })).toBeEnabled()
    expect(within(row).getByRole('button', { name: 'Cancel' })).toBeDisabled()
    expect(within(row).getByText(/A finished session cannot be cancelled/)).toBeInTheDocument()
  })

  it('disables retry and fallback for a running session, with the reason, and cancels with If-Match', async () => {
    sessions = [session({ state: 'writer_running', code: null })]
    const calls = stubApi(api)
    page()
    const row = (await screen.findByText('Writer is drafting')).closest('li') as HTMLElement
    expect(within(row).getByRole('button', { name: 'Retry' })).toBeDisabled()
    expect(within(row).getByRole('button', { name: 'Use deterministic draft' })).toBeDisabled()
    expect(within(row).getByText(/Only a failed session can be retried/)).toBeInTheDocument()
    await userEvent.click(within(row).getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(calls.some((c) => c.path.endsWith('/cancel'))).toBe(true))
    expect(calls.find((c) => c.path.endsWith('/cancel'))!.headers.get('If-Match')).toBe('"4"')
  })

  it('asks before using the deterministic draft and sends one Idempotency-Key', async () => {
    const calls = stubApi(api)
    page()
    await userEvent.click(await screen.findByRole('button', { name: 'Use deterministic draft' }))
    const dialog = await screen.findByRole('dialog')
    expect(calls.some((c) => c.path.endsWith('/fallback'))).toBe(false)
    await userEvent.click(within(dialog).getByRole('button', { name: 'Use deterministic draft' }))
    await waitFor(() => expect(calls.filter((c) => c.path.endsWith('/fallback'))).toHaveLength(1))
    expect(calls.find((c) => c.path.endsWith('/fallback'))!.headers.get('Idempotency-Key')).toBeTruthy()
  })

  it('retries with an Idempotency-Key', async () => {
    const calls = stubApi(api)
    page()
    await userEvent.click(await screen.findByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(calls.some((c) => c.path.endsWith('/retry'))).toBe(true))
    expect(calls.find((c) => c.path.endsWith('/retry'))!.headers.get('Idempotency-Key')).toBeTruthy()
  })

  it('lists proposals with translated never-auto reasons, and the section renders right to left in Persian', async () => {
    proposals = [{ id: 'kpr_1', run_id: 'run_1', trigger: 'merge_roles', bundle_artifact_id: null, state: 'awaiting_review', created_at: NOW,
      items: [{ record_id: 'kno_1', version_id: 'kvr_1', key: 'nl-rule', type: 'project', outcome: 'awaiting_review', reasons: ['roles_authored_needs_review'] }] }]
    stubApi(api)
    page()
    expect(await screen.findByText('Written by the roles author: a person must verify it')).toBeInTheDocument()
    cleanup()
    applyLocale('fa')
    page()
    expect(await screen.findByText('نوشته نویسنده نقش‌ها است: باید یک نفر آن را تأیید کند')).toBeInTheDocument()
    expect(document.documentElement.dir).toBe('rtl')
    expect(screen.queryByText('roles_authored_needs_review')).not.toBeInTheDocument()
  })
})

describe('task security badge', () => {
  const sec = (over: Record<string, unknown>) => ({ scan_id: 'scn_1', head_sha: 'abcdef0123456789', state: 'finished', completion: 'complete', open_blocking: 0, stale: false, ...over })
  const badge = (s: unknown) => render(<SecurityBadge security={s as never} />)

  it('is green only for a complete scan with no open blocking finding', () => {
    badge(sec({}))
    expect(screen.getByText('Clean')).toBeInTheDocument()
    cleanup()
    badge(sec({ completion: 'stopped_budget' }))
    expect(screen.getByText('Incomplete')).toBeInTheDocument()
    expect(screen.queryByText('Clean')).not.toBeInTheDocument()
    cleanup()
    badge(sec({ open_blocking: 2 }))
    expect(screen.getByText('2 blocking')).toBeInTheDocument()
    cleanup()
    badge(sec({ state: 'running', completion: null }))
    expect(screen.getByText('Scanning')).toBeInTheDocument()
  })

  it('marks a scan of an older commit as stale and shows nothing for a task never scanned', () => {
    badge(sec({ stale: true }))
    expect(screen.getByText('Older commit')).toBeInTheDocument()
    cleanup()
    const { container } = badge(null)
    expect(container).toBeEmptyDOMElement()
  })

  it('adds the Security column only when some task has a scan', async () => {
    stubApi(api)
    renderRoute(<TasksPage />, `/orgs/${ORG}/tasks`, '/orgs/:orgId/tasks')
    await screen.findByText('Export CSV')
    expect(screen.queryByRole('columnheader', { name: 'Security' })).not.toBeInTheDocument()
    cleanup()
    tasks = [task(sec({ open_blocking: 1 }))]
    renderRoute(<TasksPage />, `/orgs/${ORG}/tasks`, '/orgs/:orgId/tasks')
    expect(await screen.findByRole('columnheader', { name: 'Security' })).toBeInTheDocument()
    expect(screen.getByText('1 blocking')).toBeInTheDocument()
  })
})
