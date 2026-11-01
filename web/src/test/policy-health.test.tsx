import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { applyLocale } from '@/i18n/direction'
import { HealthPage } from '@/features/health/health-page'
import { OrgPolicyPanel } from '@/features/policy/policy-panel'
import { NOW, ORG, renderRoute, stubApi, type Call } from './helpers'

const base = `/api/v1/orgs/${ORG}`
const schema = [
  { key: 'automation.intake', type: 'enum', enum: ['manual', 'auto'], default: 'manual', locked: false, project_overridable: true },
  { key: 'review.require_different_model', type: 'bool', default: true, locked: true, project_overridable: false },
]
let published: boolean
let health: Record<string, unknown>

const api = (c: Call) => {
  if (c.path === `${base}/policy/schema`) return { body: schema }
  if (c.path === `${base}/policy/draft` && c.method === 'GET') return { body: { overrides: published ? { 'automation.intake': 'auto' } : {}, revision: published ? 2 : 1, differs_from_published: !published } }
  if (c.path === `${base}/policy/effective`) {
    return {
      body: {
        hash: 'x',
        items: [
          { key: 'automation.intake', value: published ? 'auto' : 'manual', default: 'manual', locked: false, source: published ? { layer: 'org', number: 3, version_id: 'pol_1' } : { layer: 'product', number: 1 } },
          { key: 'review.require_different_model', value: true, default: true, locked: true, source: { layer: 'product', number: 1 } },
        ],
      },
    }
  }
  if (c.path === `${base}/policy/versions`) return { body: { items: [], next_cursor: null } }
  if (c.path === `${base}/policy/publish`) {
    published = true
    return { status: 201, body: { id: 'pol_1', number: 3, created_at: NOW } }
  }
  if (c.path === `${base}/health`) return { body: health }
  return undefined
}
const healthBody = (catherd: Record<string, unknown>) => ({
  mongo: { ok: true, replica_set: 'rs0' }, queue: { queued: 0, leased: 0, failed_24h: 0, dead_24h: 0 }, runner: { alive: true }, connections: [], catherd,
})

beforeEach(() => {
  applyLocale('en')
  published = false
})
afterEach(() => {
  vi.unstubAllGlobals()
  applyLocale('en')
})

describe('policy panel', () => {
  it('shows a lock and no edit control for a locked key', async () => {
    stubApi(api)
    renderRoute(<OrgPolicyPanel orgId={ORG} />, '/x', '/x')
    const row = (await screen.findByText('review.require_different_model')).closest('tr') as HTMLElement
    expect(within(row).getByText('Locked')).toBeInTheDocument()
    expect(within(row).queryByRole('switch')).toBeNull()
    expect(within(row).queryByRole('combobox')).toBeNull()
    const editable = screen.getByText('automation.intake').closest('tr') as HTMLElement
    expect(within(editable).getByRole('combobox')).toBeInTheDocument()
  })

  it('changes the source badge after a publish', async () => {
    const calls = stubApi(api)
    renderRoute(<OrgPolicyPanel orgId={ORG} />, '/x', '/x')
    const row = async () => (await screen.findByText('automation.intake')).closest('tr') as HTMLElement
    expect(within(await row()).getByText('Product default')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Publish' }))
    await waitFor(() => expect(within(screen.getByText('automation.intake').closest('tr') as HTMLElement).getByText(/Organization/)).toBeInTheDocument())
    expect(within(screen.getByText('automation.intake').closest('tr') as HTMLElement).getByText(/v3/)).toBeInTheDocument()
    const post = calls.find((c) => c.path.endsWith('/policy/publish'))
    expect(post?.headers.get('Idempotency-Key')).toBeTruthy()
    expect(post?.headers.get('If-Match')).toBe('"1"')
  })
})

describe('health page', () => {
  it('shows the degraded state when catherd is unavailable', async () => {
    health = healthBody({ available: false, supported: false, home_display: '~', last_doctor: null })
    stubApi(api)
    renderRoute(<HealthPage />, `/orgs/${ORG}/health`, '/orgs/:orgId/health')
    expect(await screen.findByText(/catherd is not available/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Run doctor' })).toBeDisabled()
  })
})
