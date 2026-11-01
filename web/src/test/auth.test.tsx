import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import '@/i18n'
import { RequireSession } from '@/features/auth/require-session'
import { resetHashToken, takeHashToken } from '@/lib/hash-token'

afterEach(() => {
  vi.unstubAllGlobals()
  resetHashToken()
  window.history.replaceState(null, '', '/')
})

function mockStatus(body: object) {
  vi.stubGlobal(
    'fetch',
    vi.fn().mockImplementation(() => Promise.resolve(Response.json(body))),
  )
}

function renderGuarded() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={['/orgs']}>
        <Routes>
          <Route element={<RequireSession />}>
            <Route path="/orgs" element={<p>secret page</p>} />
          </Route>
          <Route path="/bootstrap" element={<p>bootstrap page</p>} />
          <Route path="/login" element={<p>login page</p>} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

describe('RequireSession', () => {
  it('sends a fresh install to /bootstrap', async () => {
    mockStatus({ bootstrapped: false, authenticated: false })
    renderGuarded()
    expect(await screen.findByText('bootstrap page')).toBeInTheDocument()
  })
  it('sends an anonymous browser to /login', async () => {
    mockStatus({ bootstrapped: true, authenticated: false })
    renderGuarded()
    expect(await screen.findByText('login page')).toBeInTheDocument()
  })
  it('renders the page for a signed-in session', async () => {
    mockStatus({ bootstrapped: true, authenticated: true, csrf_token: 'c' })
    renderGuarded()
    expect(await screen.findByText('secret page')).toBeInTheDocument()
  })
})

describe('takeHashToken', () => {
  it('reads the fragment token once and strips it from the address bar', () => {
    window.history.replaceState(null, '', '/bootstrap#token=abc123')
    expect(takeHashToken()).toBe('abc123')
    expect(window.location.hash).toBe('')
    expect(takeHashToken()).toBe('abc123')
  })
})
