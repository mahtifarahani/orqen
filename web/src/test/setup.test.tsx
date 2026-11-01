import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { HomeRedirect } from '@/features/home-redirect'
import { PendingSetupCard } from '@/features/overview/pending-setup-card'
import { SetupPage } from '@/features/setup/setup-page'
import { applyLocale } from '@/i18n/direction'
import { NOW, ORG, problem, renderRoute, stubApi, type Call } from './helpers'

const base = `/api/v1/orgs/${ORG}`
const step = (key: string, status = 'todo', extra: Record<string, unknown> = {}) => ({ key, status, attention: false, missing: [], consequences: [], skip_reason: null, ...extra })
const KEYS = ['organization', 'connections', 'projects', 'profile', 'policy', 'system', 'review']
let wizard: Record<string, unknown>
let orgs: unknown[]
let completeResult: ReturnType<typeof problem> | { body: unknown }

const mkWizard = (over: Record<string, unknown> = {}) => ({
  current_step: 'organization', dismissed: false, completed_at: null, pending_count: 7, revision: 3,
  steps: KEYS.map((k) => step(k, 'todo', k === 'system' ? { consequences: ['issues_undetected'], missing: [{ code: 'no_recent_preflight', detail: 'x' }] } : {})),
  ...over,
})
const api = (c: Call) => {
  if (c.path === '/api/v1/orgs') return { body: { items: orgs, next_cursor: null } }
  if (c.path === `${base}/setup` && c.method === 'GET') return { body: wizard }
  if (c.path === `${base}/setup` && c.method === 'PATCH') return { body: { ...wizard, current_step: (c.body as { current_step?: string }).current_step ?? wizard.current_step, revision: 4 } }
  if (c.path === `${base}/setup/steps/system/complete`) return completeResult
  if (c.path === `${base}/setup/steps/system/skip`) return { body: mkWizard({ revision: 4, steps: KEYS.map((k) => step(k, k === 'system' ? 'skipped' : 'todo')) }) }
  return undefined
}
const org = { id: ORG, slug: 'a', name: 'A', locale: 'en', timezone: 'UTC', revision: 1, created_at: NOW, updated_at: NOW }

function Where() {
  return <div data-testid="where">{useLocation().pathname}</div>
}
function renderHome() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={['/']}>
        <Routes>
          <Route path="/" element={<HomeRedirect />} />
          <Route path="*" element={<Where />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}
const renderSetup = (search = '') => renderRoute(<SetupPage />, `/orgs/${ORG}/setup${search}`, '/orgs/:orgId/setup')

beforeEach(() => {
  applyLocale('en')
  wizard = mkWizard()
  orgs = [org]
  completeResult = problem(409, 'wizard_step_incomplete', { errors: [{ field: 'no_recent_preflight', message: 'no system check' }] })
})
afterEach(() => {
  vi.unstubAllGlobals()
  applyLocale('en')
})

describe('first-run redirect', () => {
  it('goes to /setup/new when there is no organization', async () => {
    orgs = []
    stubApi(api)
    renderHome()
    expect(await screen.findByTestId('where')).toHaveTextContent('/setup/new')
  })
  it('goes to the wizard while it is unfinished', async () => {
    stubApi(api)
    renderHome()
    expect(await screen.findByTestId('where')).toHaveTextContent(`/orgs/${ORG}/setup`)
  })
  it('goes to the overview when the wizard is dismissed or finished', async () => {
    wizard = mkWizard({ dismissed: true })
    stubApi(api)
    renderHome()
    expect(await screen.findByTestId('where')).toHaveTextContent(`/orgs/${ORG}/overview`)
  })
})

describe('wizard', () => {
  it('opens the step the server remembers', async () => {
    wizard = mkWizard({ current_step: 'system' })
    stubApi(api)
    renderSetup()
    expect(await screen.findByRole('heading', { level: 2, name: 'System check' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /System check/, current: 'step' })).toBeInTheDocument()
  })

  it('skip shows the consequences and only calls skip after the user confirms', async () => {
    wizard = mkWizard({ current_step: 'system' })
    const calls = stubApi(api)
    renderSetup('?step=system')
    await userEvent.click(await screen.findByRole('button', { name: 'Skip this step' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('Problems with this machine will not be detected ahead of time.')).toBeInTheDocument()
    expect(within(dialog).getByText('No system check from the last 24 hours.')).toBeInTheDocument()
    expect(calls.some((c) => c.path.endsWith('/skip'))).toBe(false)
    await userEvent.click(within(dialog).getByRole('button', { name: 'Skip this step' }))
    await waitFor(() => expect(calls.some((c) => c.path.endsWith('/steps/system/skip'))).toBe(true))
    const skip = calls.find((c) => c.path.endsWith('/skip'))!
    expect(skip.headers.get('If-Match')).toBe('"3"')
  })

  it('Cancel in the skip dialog skips nothing', async () => {
    const calls = stubApi(api)
    renderSetup('?step=system')
    await userEvent.click(await screen.findByRole('button', { name: 'Skip this step' }))
    await userEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Cancel' }))
    expect(calls.some((c) => c.path.endsWith('/skip'))).toBe(false)
  })

  it('Next on a 409 lists what is missing and stays on the step', async () => {
    stubApi(api)
    renderSetup('?step=system')
    await userEvent.click(await screen.findByRole('button', { name: 'Next' }))
    const list = await screen.findByTestId('missing-list')
    expect(within(list).getByText('No system check from the last 24 hours.')).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 2, name: 'System check' })).toBeInTheDocument()
  })

  it('in Persian the page is rtl and the stepper chevrons mirror', async () => {
    applyLocale('fa')
    stubApi(api)
    renderSetup('?step=system')
    const stepper = await screen.findByTestId('stepper')
    expect(document.documentElement.dir).toBe('rtl')
    expect(stepper.querySelector('svg.rtl\\:rotate-180')).not.toBeNull()
  })
})

describe('pending setup card', () => {
  it('lists skipped steps with their consequence text', async () => {
    wizard = mkWizard({ pending_count: 1, steps: KEYS.map((k) => step(k, k === 'system' ? 'skipped' : 'done', k === 'system' ? { consequences: ['issues_undetected'] } : {})) })
    stubApi(api)
    renderRoute(<PendingSetupCard />, `/orgs/${ORG}/overview`, '/orgs/:orgId/overview')
    expect(await screen.findByText('Pending setup')).toBeInTheDocument()
    expect(screen.getByText('Problems with this machine will not be detected ahead of time.')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /Resume setup/ })).toHaveAttribute('href', `/orgs/${ORG}/setup?step=system`)
  })
  it('renders nothing when nothing is pending', async () => {
    wizard = mkWizard({ pending_count: 0, steps: KEYS.map((k) => step(k, 'done')) })
    const calls = stubApi(api)
    const view = renderRoute(<PendingSetupCard />, `/orgs/${ORG}/overview`, '/orgs/:orgId/overview')
    await waitFor(() => expect(calls.length).toBeGreaterThan(0))
    expect(view.container).toBeEmptyDOMElement()
  })
})
