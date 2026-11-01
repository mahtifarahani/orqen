import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import '@/i18n'
import { OrgSettingsPage } from '@/features/orgs/org-settings-page'

const org = (revision: number, name: string) => ({
  id: 'org_X',
  slug: 'acme',
  name,
  locale: 'en',
  timezone: 'UTC',
  revision,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
})

afterEach(() => vi.unstubAllGlobals())

describe('OrgSettingsPage', () => {
  it('shows the 409 conflict UI and keeps the typed value', async () => {
    const calls: { method: string; ifMatch: string | null }[] = []
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation((_url: string, init: RequestInit) => {
        const method = init.method ?? 'GET'
        calls.push({ method, ifMatch: new Headers(init.headers).get('If-Match') })
        if (method === 'GET') return Promise.resolve(Response.json(org(1, 'Acme')))
        return Promise.resolve(
          new Response(
            JSON.stringify({
              type: 't',
              title: 'Conflict',
              status: 409,
              code: 'revision_conflict',
              detail: 'changed',
              current: org(2, 'Acme (other tab)'),
            }),
            { status: 409, headers: { 'content-type': 'application/problem+json' } },
          ),
        )
      }),
    )
    const user = userEvent.setup()
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    render(
      <QueryClientProvider client={qc}>
        <MemoryRouter initialEntries={['/orgs/org_X/settings']}>
          <Routes>
            <Route path="/orgs/:orgId/settings" element={<OrgSettingsPage />} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    )
    const name = await screen.findByLabelText('Name')
    await user.clear(name)
    await user.type(name, 'Mine')
    await user.click(screen.getByRole('button', { name: 'Save changes' }))

    expect(await screen.findByTestId('conflict-alert')).toBeInTheDocument()
    expect(calls.find((c) => c.method === 'PATCH')?.ifMatch).toBe('"1"')
    // the user's text is still there
    expect(screen.getByLabelText('Name')).toHaveValue('Mine')

    await user.click(screen.getByRole('button', { name: 'Load latest' }))
    await waitFor(() => expect(screen.getByLabelText('Name')).toHaveValue('Acme (other tab)'))
    expect(screen.queryByTestId('conflict-alert')).not.toBeInTheDocument()
  })
})
