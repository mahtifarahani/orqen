import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { InboxPage } from '@/features/inbox/inbox-page'
import { applyLocale } from '@/i18n/direction'
import { NOW, ORG, problem, renderRoute, stubApi, type Call } from './helpers'

const base = `/api/v1/orgs/${ORG}`
const item = (over: Record<string, unknown> = {}) => ({
  id: 'inb_1', run_id: 'run_1', project_id: 'prj_1', task_id: 'iss_1', issue_key: 'ORQ-2', kind: 'question', reason: 'missing_acceptance', question_code: 'missing_acceptance', params: {}, stage: 'triage', priority: 'high',
  evidence: [{ source_id: 'isn_1', kind: 'jira_description', label: 'description', excerpt: 'Make search better', url: 'http://jira/browse/ORQ-2' }],
  options: [{ id: 'provide_acceptance', requires_text: true, effect_code: 'add_acceptance' }, { id: 'cancel', requires_text: false, effect_code: 'cancel_run' }],
  answers: [], status: 'open', resolution: '', superseded_by_run: null, created_at: NOW, updated_at: NOW, revision: 1, ...over,
})
let answerResult: { status?: number; body?: unknown }
const api = (c: Call) => {
  if (c.path === `${base}/inbox` && c.method === 'GET') return { body: { items: [item()], next_cursor: null } }
  if (c.path === `${base}/inbox/inb_1` && c.method === 'GET') return { body: item() }
  if (c.path === `${base}/projects`) return { body: { items: [], next_cursor: null } }
  if (c.path === `${base}/inbox/inb_1/answer`) return answerResult
  return undefined
}
const render = (search = '') => renderRoute(<InboxPage />, `/orgs/${ORG}/inbox${search}`, '/orgs/:orgId/inbox')
beforeEach(() => {
  applyLocale('en')
  answerResult = { body: { item: item({ status: 'resolved', revision: 2 }), run: {} } }
})
afterEach(() => {
  vi.unstubAllGlobals()
  applyLocale('en')
})

describe('inbox', () => {
  it('lists open items and the filters become query parameters', async () => {
    const calls = stubApi(api)
    render()
    expect(await screen.findByText(/no acceptance criteria/)).toBeInTheDocument()
    const first = calls.find((c) => c.path === `${base}/inbox`)
    expect(first?.search.get('status')).toBe('open')
    await userEvent.click(screen.getByLabelText('Status'))
    await userEvent.click(await screen.findByRole('option', { name: 'Resolved' }))
    await waitFor(() => expect(calls.some((c) => c.path === `${base}/inbox` && c.search.get('status') === 'resolved')).toBe(true))
  })
  it('answers with choice and text, If-Match and one Idempotency-Key; a double click sends one request', async () => {
    const calls = stubApi(api)
    render('?item=inb_1')
    const dialog = await screen.findByRole('dialog')
    const send = await within(dialog).findByRole('button', { name: 'Answer and resume' })
    expect(send).toBeDisabled() // the chosen option needs a text
    await userEvent.type(await within(dialog).findByLabelText('Your answer'), 'Results are filtered by date.')
    await userEvent.dblClick(send)
    await waitFor(() => expect(calls.filter((c) => c.path.endsWith('/answer'))).toHaveLength(1))
    const post = calls.find((c) => c.path.endsWith('/answer'))
    expect(post?.body).toEqual({ choice: 'provide_acceptance', text: 'Results are filtered by date.' })
    expect(post?.headers.get('If-Match')).toBe('"1"')
    expect(post?.headers.get('Idempotency-Key')).toBeTruthy()
  })
  it('sends a publish approval to the approval dialog of the task instead of answering it here', async () => {
    const approval = item({ kind: 'approval', reason: 'publish_approval', question_code: 'publish_approval', stage: 'publish', options: [{ id: 'approve_all', requires_text: false, effect_code: 'approve_all' }, { id: 'reject', requires_text: false, effect_code: 'reject' }] })
    const calls = stubApi((c) => (c.path === `${base}/inbox/inb_1` && c.method === 'GET' ? { body: approval } : api(c)))
    render('?item=inb_1')
    const dialog = await screen.findByRole('dialog')
    const link = await within(dialog).findByRole('link', { name: 'Open the approval dialog' })
    expect(link).toHaveAttribute('href', `/orgs/${ORG}/tasks/iss_1?tab=publishing`)
    expect(within(dialog).queryByRole('button', { name: 'Submit' })).toBeNull()
    expect(calls.some((c) => c.path.endsWith('/answer'))).toBe(false)
    await userEvent.click(within(dialog).getByLabelText(/Reject and keep dry-run/))
    expect(await within(dialog).findByRole('button', { name: 'Submit' })).toBeEnabled()
  })
  it('shows the stale alert with the text kept on 409 inbox_item_stale', async () => {
    answerResult = problem(409, 'inbox_item_stale', { current: item({ status: 'superseded', superseded_by_run: 'run_9' }) })
    stubApi(api)
    render('?item=inb_1')
    const dialog = await screen.findByRole('dialog')
    await userEvent.type(await within(dialog).findByLabelText('Your answer'), 'my carefully written text')
    await userEvent.click(within(dialog).getByRole('button', { name: 'Answer and resume' }))
    const alert = await screen.findByTestId('stale-alert')
    expect(within(alert).getByText('This question was replaced')).toBeInTheDocument()
    expect(within(alert).getByDisplayValue('my carefully written text')).toBeInTheDocument()
    expect(within(alert).getByRole('link', { name: 'Open the current task' })).toBeInTheDocument()
  })
  it('shows the conflict alert on 409 revision_conflict', async () => {
    answerResult = problem(409, 'revision_conflict', { current: item({ revision: 5 }) })
    stubApi(api)
    render('?item=inb_1')
    const dialog = await screen.findByRole('dialog')
    await userEvent.type(await within(dialog).findByLabelText('Your answer'), 'text')
    await userEvent.click(within(dialog).getByRole('button', { name: 'Answer and resume' }))
    expect(await screen.findByTestId('conflict-alert')).toBeInTheDocument()
  })
  it('opens the sheet on the end side, which is the left in Persian, and Escape closes it', async () => {
    applyLocale('fa')
    stubApi(api)
    render('?item=inb_1')
    const dialog = await screen.findByRole('dialog')
    expect(dialog).toHaveAttribute('data-side', 'end')
    expect(document.documentElement.dir).toBe('rtl')
    await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })
})
