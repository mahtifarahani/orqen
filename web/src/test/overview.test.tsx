import { screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { applyLocale } from '@/i18n/direction'
import { OverviewPage } from '@/features/overview/overview-page'
import { EventsContext } from '@/components/events-context'
import { NOW, ORG, renderRoute, stubApi, type Call } from './helpers'

const base = `/api/v1/orgs/${ORG}`
const summary = (state: string, i: number) => ({ id: `prj_${i}`, name: `p${i}`, state, blocker_count: 0, automation_enabled: state.startsWith('enabled'), revision: 1, primary_repo: null })
let catherd: Record<string, unknown>
const api = (c: Call) => {
  if (c.path === `${base}/connections`) return { body: { items: [], next_cursor: null } }
  if (c.path === `${base}/projects`) return { body: { items: [summary('ready', 1), summary('enabled', 2), summary('enabled_blocked', 3), summary('enabled', 4)], next_cursor: null } }
  if (c.path === `${base}/catherd/status`) return { body: catherd }
  return undefined
}
const feed = { status: 'live' as const, events: [] }
beforeEach(() => applyLocale('en'))
afterEach(() => vi.unstubAllGlobals())

describe('overview cards', () => {
  it('counts projects by state and shows the catherd status', async () => {
    catherd = { available: true, supported: true, version: '1.5.0', home_display: '~', last_doctor: { ready: true, checked_at: NOW, checks: [], operation_id: 'op', artifact_id: 'art' } }
    stubApi(api)
    renderRoute(<EventsContext.Provider value={feed}><OverviewPage /></EventsContext.Provider>, `/orgs/${ORG}/overview`, '/orgs/:orgId/overview')
    const list = await screen.findByRole('list', { name: 'Projects' })
    expect(within(list.querySelector('[data-state="enabled"]')!.closest('li') as HTMLElement).getByText('2')).toBeInTheDocument()
    expect(await screen.findByText('Supported')).toBeInTheDocument()
    expect(screen.getAllByText('Ready').length).toBeGreaterThan(0)
  })

  it('shows catherd unavailable as a degraded card', async () => {
    catherd = { available: false, supported: false, home_display: '~', last_doctor: null }
    stubApi(api)
    renderRoute(<EventsContext.Provider value={feed}><OverviewPage /></EventsContext.Provider>, `/orgs/${ORG}/overview`, '/orgs/:orgId/overview')
    expect(await screen.findByText('catherd is not available')).toBeInTheDocument()
  })
})
