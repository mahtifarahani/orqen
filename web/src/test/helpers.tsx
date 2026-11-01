import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render } from '@testing-library/react'
import type { ReactNode } from 'react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { vi } from 'vitest'
import { LocaleDirection } from '@/components/locale-direction'
import { TooltipProvider } from '@/components/ui/tooltip'
import '@/i18n'

export interface Call {
  method: string
  path: string
  search: URLSearchParams
  headers: Headers
  body: unknown
}
export type Handler = (call: Call) => { status?: number; body?: unknown } | undefined
export const problem = (status: number, code: string, extra: Record<string, unknown> = {}) => ({
  status,
  body: { type: 't', title: code, status, code, detail: code, ...extra },
})

/** Replaces `fetch` with a router over `handlers`; unknown requests answer 404. Returns the call log. */
export function stubApi(handlers: (call: Call) => ReturnType<Handler>) {
  const calls: Call[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn().mockImplementation((url: string, init: RequestInit = {}) => {
      const u = new URL(url, 'http://x')
      let body: unknown
      try {
        body = init.body ? JSON.parse(String(init.body)) : undefined
      } catch {
        body = init.body
      }
      const call: Call = { method: (init.method ?? 'GET').toUpperCase(), path: u.pathname, search: u.searchParams, headers: new Headers(init.headers), body }
      calls.push(call)
      const r = handlers(call) ?? problem(404, 'not_found')
      return Promise.resolve(Response.json(r.body ?? {}, { status: r.status ?? 200 }))
    }),
  )
  return calls
}

export function renderRoute(element: ReactNode, path: string, routePath: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <LocaleDirection>
      <TooltipProvider>
        <MemoryRouter initialEntries={[path]}>
          <Routes>
            <Route path={routePath} element={element} />
          </Routes>
        </MemoryRouter>
      </TooltipProvider>
      </LocaleDirection>
    </QueryClientProvider>,
  )
}

export const ORG = 'org_X'
export const NOW = '2026-01-02T03:04:05Z'
