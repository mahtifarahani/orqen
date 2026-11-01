import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import '@/i18n'
import { applyLocale } from '@/i18n/direction'
import { DiscoverPage } from '@/features/projects/discover-page'
import { ProjectsPage } from '@/features/projects/projects-page'

const ORG = 'org_X'
const CON = 'con_G'
const org = { id: ORG, slug: 'acme', name: 'Acme', locale: 'en', timezone: 'UTC', revision: 1, created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z' }
const connection = {
  id: CON, org_id: ORG, name: 'GitLab', provider: 'gitlab', deployment: 'self_managed', base_url: 'http://gitlab.test', auth_method: 'personal_access_token',
  timeout_ms: 15000, max_concurrency: 4, allowed_hosts: ['gitlab.test'], status: 'verified', credential: { state: 'set' }, readiness: { unattended_ready: true, reasons: [] },
  revision: 1, created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z',
}
const row = (n: string, extra: Record<string, unknown> = {}) => ({
  id: `cpj_${n}`, connection_id: CON, provider_project_id: n, name: `proj-${n}`, path_with_namespace: `group-1/proj-${n}`, default_branch: 'main', web_url: 'x',
  archived: false, visibility: 'private', last_activity_at: '2026-01-02T03:04:05Z', gone: false, last_seen_at: '2026-01-02T03:04:05Z', project_id: null, ...extra,
})
const summary = [{ connection_id: CON, name: 'GitLab', project_count: 4, refreshed_at: new Date().toISOString(), running_operation_id: null }]

interface Posts {
  body: { connection_id: string; provider_project_ids: string[] }
  key: string | null
}
let posts: Posts[]
let failPost: boolean
let catalogMode: 'pages' | 'empty' | 'fail'

function stubFetch() {
  vi.stubGlobal(
    'fetch',
    vi.fn().mockImplementation((url: string, init: RequestInit = {}) => {
      const method = init.method ?? 'GET'
      const u = new URL(url, 'http://x')
      const json = (body: unknown, status = 200) => Promise.resolve(Response.json(body, { status }))
      if (u.pathname === `/api/v1/orgs/${ORG}`) return json(org)
      if (u.pathname === `/api/v1/orgs/${ORG}/connections`) return json({ items: [connection], next_cursor: null })
      if (u.pathname === `/api/v1/orgs/${ORG}/catalog/summary`) return json(summary)
      if (u.pathname === `/api/v1/orgs/${ORG}/connections/${CON}/catalog-projects`) {
        if (catalogMode === 'fail') return json({ type: 't', title: 'x', status: 500, code: 'internal', detail: 'boom' }, 500)
        if (catalogMode === 'empty') return json({ items: [], next_cursor: null })
        return u.searchParams.get('cursor') === 'c2'
          ? json({ items: [row('3'), row('4')], next_cursor: null })
          : json({ items: [row('1'), row('2', { project_id: 'prj_2' })], next_cursor: 'c2' })
      }
      if (method === 'POST' && u.pathname === `/api/v1/orgs/${ORG}/projects/from-catalog`) {
        posts.push({ body: JSON.parse(String(init.body)), key: new Headers(init.headers).get('Idempotency-Key') })
        if (failPost) return json({ type: 't', title: 'x', status: 500, code: 'internal', detail: 'boom' }, 500)
        return json({ created: [], existing: [], missing: [] })
      }
      if (u.pathname === `/api/v1/orgs/${ORG}/projects`) {
        return json({
          items: [{ id: 'prj_1', name: 'proj-1', primary_repo: { path: 'group-1/proj-1', connection_id: CON }, state: 'enabled_blocked', blocker_count: 3, automation_enabled: true, revision: 2 }],
          next_cursor: u.searchParams.get('cursor') ? null : 'p2',
        })
      }
      return json({ type: 't', title: 'Not found', status: 404, code: 'not_found', detail: url }, 404)
    }),
  )
}
function renderPage(element: React.ReactNode, path: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/orgs/:orgId/projects" element={element} />
          <Route path="/orgs/:orgId/projects/discover" element={element} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}
const discover = () => renderPage(<DiscoverPage />, `/orgs/${ORG}/projects/discover`)

beforeEach(() => {
  posts = []
  failPost = false
  catalogMode = 'pages'
  applyLocale('en')
  stubFetch()
})
afterEach(() => {
  vi.unstubAllGlobals()
  applyLocale('en')
})

describe('DiscoverPage', () => {
  it('selects across two pages and sends both ids once', async () => {
    const user = userEvent.setup()
    discover()
    await user.click(await screen.findByRole('checkbox', { name: 'Select proj-1' }))
    await user.click(screen.getByRole('button', { name: 'Load more' }))
    await user.click(await screen.findByRole('checkbox', { name: 'Select proj-3' }))
    expect(screen.getByText('2 selected')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Add selected (2)' }))
    await waitFor(() => expect(posts).toHaveLength(1))
    expect(posts[0].body).toEqual({ connection_id: CON, provider_project_ids: ['1', '3'] })
    expect(posts[0].key).toBeTruthy()
    // The selection is cleared after a successful add.
    await waitFor(() => expect(screen.queryByText('2 selected')).not.toBeInTheDocument())
  })
  it('keeps the idempotency key on retry and renews it when the selection changes', async () => {
    failPost = true
    const user = userEvent.setup()
    discover()
    await user.click(await screen.findByRole('checkbox', { name: 'Select proj-1' }))
    await user.click(screen.getByRole('button', { name: 'Add selected (1)' }))
    await waitFor(() => expect(posts).toHaveLength(1))
    await screen.findByRole('alert')
    await user.click(screen.getByRole('button', { name: 'Add selected (1)' }))
    await waitFor(() => expect(posts).toHaveLength(2))
    expect(posts[1].key).toBe(posts[0].key)
    await user.click(screen.getByRole('button', { name: 'Load more' }))
    await user.click(await screen.findByRole('checkbox', { name: 'Select proj-3' }))
    await user.click(screen.getByRole('button', { name: 'Add selected (2)' }))
    await waitFor(() => expect(posts).toHaveLength(3))
    expect(posts[2].key).not.toBe(posts[0].key)
  })
  it('selects every selectable loaded row from the header checkbox', async () => {
    const user = userEvent.setup()
    discover()
    await user.click(await screen.findByRole('checkbox', { name: 'Select all loaded projects' }))
    expect(screen.getByText('1 selected')).toBeInTheDocument()
    expect(screen.getByRole('checkbox', { name: 'Select proj-1' })).toBeChecked()
  })
  it('marks configured rows with a link and does not let them be selected', async () => {
    discover()
    const badge = await screen.findByRole('link', { name: 'Configured' })
    expect(badge).toHaveAttribute('href', `/orgs/${ORG}/projects/prj_2`)
    expect(screen.getByRole('checkbox', { name: 'Select proj-2' })).toBeDisabled()
  })
  it('shows the empty and failed states', async () => {
    catalogMode = 'empty'
    discover()
    expect(await screen.findByText('The catalog is empty.')).toBeInTheDocument()
  })
  it('shows a retryable error when the catalog cannot be loaded', async () => {
    catalogMode = 'fail'
    discover()
    expect(await screen.findByText('Could not load the catalog.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Retry' })).toBeInTheDocument()
  })
  it('renders right-to-left in Persian with paths isolated as left-to-right', async () => {
    applyLocale('fa')
    discover()
    const path = await screen.findByText('group-1/proj-1')
    expect(document.documentElement.dir).toBe('rtl')
    expect(path.closest('bdi')).toHaveAttribute('dir', 'ltr')
    expect(screen.getByRole('table')).toBeInTheDocument()
  })
})

describe('ProjectsPage', () => {
  it('lists projects with state text, blockers and a load-more button', async () => {
    renderPage(<ProjectsPage />, `/orgs/${ORG}/projects`)
    const link = await screen.findByRole('link', { name: 'proj-1' })
    expect(link).toHaveAttribute('href', `/orgs/${ORG}/projects/prj_1`)
    const rowEl = link.closest('tr') as HTMLElement
    expect(within(rowEl).getByText('Enabled, blocked')).toBeInTheDocument()
    expect(within(rowEl).getByText('3')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Load more' })).toBeInTheDocument()
  })
  it('explains the empty state with a way to discover projects', async () => {
    vi.stubGlobal('fetch', vi.fn().mockImplementation(() => Promise.resolve(Response.json({ items: [], next_cursor: null }))))
    renderPage(<ProjectsPage />, `/orgs/${ORG}/projects`)
    expect(await screen.findByText(/No projects yet/)).toBeInTheDocument()
    expect(screen.getAllByRole('link', { name: 'Discover projects' }).length).toBeGreaterThan(0)
  })
  it('shows a retryable error', async () => {
    vi.stubGlobal('fetch', vi.fn().mockImplementation(() => Promise.resolve(Response.json({ type: 't', title: 'x', status: 500, code: 'internal', detail: 'boom' }, { status: 500 }))))
    renderPage(<ProjectsPage />, `/orgs/${ORG}/projects`)
    expect(await screen.findByText('Could not load projects.')).toBeInTheDocument()
  })
})
