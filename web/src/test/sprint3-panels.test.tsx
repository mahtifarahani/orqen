import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AutomationCard } from '@/features/overview/automation-card'
import { InboxCard, IntakeCard } from '@/features/overview/work-cards'
import { RulesPanel } from '@/features/rules/rules-panel'
import { applyLocale } from '@/i18n/direction'
import { NOW, ORG, renderRoute, stubApi, type Call } from './helpers'

const base = `/api/v1/orgs/${ORG}`
let paused = false
const api = (c: Call) => {
  if (c.path === `${base}/automation` && c.method === 'GET') return { body: { paused, paused_at: null, reason: null, revision: paused ? 1 : 0 } }
  if (c.path === `${base}/automation` && c.method === 'PUT') return { body: { paused: true, paused_at: NOW, reason: null, revision: 1 } }
  if (c.path === `${base}/tasks/summary`)
    return { body: { runs_by_state: { triaging: 2, needs_input: 1 }, inbox_open: 1, inbox_open_by_reason: {}, candidates: 3, intake: [{ project_id: 'prj_1', last_success_at: NOW, last_error: { code: 'rate_limited', detail: 'x', at: NOW }, waiting_network: false, blocked: null }], decisions_24h: {}, workspaces_by_state: {}, evaluated_at: NOW } }
  if (c.path === `${base}/projects`) return { body: { items: [{ id: 'prj_1', name: 'App', primary_repo: null, state: 'ready', blocker_count: 0, automation_enabled: true, revision: 1 }], next_cursor: null } }
  if (c.path === `${base}/inbox`) return { body: { items: [], next_cursor: null } }
  if (c.path === `${base}/rules` && c.method === 'GET') return { body: [{ id: 'rul_1', scope: 'org', project_id: null, title: 'Be careful', body: 'Never log tokens.', critical: true, revision: 3, created_at: NOW, updated_at: NOW }] }
  if (c.path === `${base}/rules` && c.method === 'POST') return { status: 201, body: { id: 'rul_2', scope: 'org', project_id: null, title: 'T', body: 'B', critical: false, revision: 1, created_at: NOW, updated_at: NOW } }
  return undefined
}
beforeEach(() => {
  applyLocale('en')
  paused = false
})
afterEach(() => vi.unstubAllGlobals())

describe('Sprint 3 panels', () => {
  it('the automation card pauses with If-Match 0 for a state that does not exist yet', async () => {
    const calls = stubApi(api)
    renderRoute(<AutomationCard orgId={ORG} />, `/orgs/${ORG}/overview`, '/orgs/:orgId/overview')
    await userEvent.click(await screen.findByRole('button', { name: 'Pause automation' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true))
    const put = calls.find((c) => c.method === 'PUT')
    expect(put?.headers.get('If-Match')).toBe('"0"')
    expect(put?.body).toEqual({ paused: true })
  })
  it('the intake card shows a rate-limited project and the work card groups runs', async () => {
    stubApi(api)
    renderRoute(
      <>
        <IntakeCard orgId={ORG} />
        <InboxCard orgId={ORG} />
      </>,
      `/orgs/${ORG}/overview`,
      '/orgs/:orgId/overview',
    )
    expect(await screen.findByText('Jira is rate limiting Orqen')).toBeInTheDocument()
    expect(await screen.findByText('Nothing is waiting for you.')).toBeInTheDocument()
  })
  it('rules: lists, adds with the typed fields, and keeps the critical switch off by default', async () => {
    const calls = stubApi(api)
    renderRoute(<RulesPanel orgId={ORG} />, `/orgs/${ORG}/settings`, '/orgs/:orgId/settings')
    expect(await screen.findByText('Be careful')).toBeInTheDocument()
    await userEvent.type(screen.getByLabelText('Title'), 'T')
    await userEvent.type(screen.getByLabelText('Rule'), 'B')
    await userEvent.click(screen.getByRole('button', { name: 'Add rule' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true))
    expect(calls.find((c) => c.method === 'POST')?.body).toEqual({ scope: 'org', project_id: null, title: 'T', body: 'B', critical: false })
  })
})
