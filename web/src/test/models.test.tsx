import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { applyLocale } from '@/i18n/direction'
import { TooltipProvider } from '@/components/ui/tooltip'
import { IndependenceBadge } from '@/features/models/independence-badge'
import { ProfileEditorPage } from '@/features/models/profile-editor-page'
import { NOW, ORG, renderRoute, stubApi, type Call } from './helpers'

const MPF = 'mpf_A'
const base = `/api/v1/orgs/${ORG}`
const doc = {
  schema: 1,
  objective: 'cost',
  roles: { worker: { enabled: true, access: 'workspace-write', rungs: ['codex:gpt-6-luna#high'] }, reviewer: { enabled: true, access: 'read-only', rungs: ['codex:gpt-6-sol#high'] } },
  budget: { usd: 5 },
}
const profile = (over: Record<string, unknown> = {}) => ({
  id: MPF, slug: 'team', name: 'Team', description: null, revision: 2, created_at: NOW, updated_at: NOW,
  draft: { doc, meta: { budget_uncapped_ack: false }, updated_at: NOW }, draft_hash: 'h1', latest_version_id: null, source: { kind: 'blank', ref: null },
  last_validation: { catherd_version: '1.5.0', checked_at: NOW, draft_hash: 'h1', errors: [], warnings: [], orqen_issues: [], valid: true, operation_id: 'op_1', independence: { status: 'independent', shared: [], unmatched: [] } },
  ...over,
})
const rung = (name: string, over: Record<string, unknown> = {}) => ({ rung: name, effort: 'high', scored: true, availability: 'available', reasons: [], scores: {}, inferred_only: false, cost_tier: 2, ...over })
const catalog = {
  refreshed_at: NOW,
  models: [
    { family_id: 'f1', backend: 'codex', model: 'gpt-6-sol', name: 'GPT-6 Sol', billing: 'chatgpt-plan', efforts: ['high'], roles: ['worker'], listed: true, rungs: [rung('codex:gpt-6-sol#high'), rung('codex:gpt-6-sol#low', { availability: 'not_offered', reasons: ['account'] })] },
  ],
}
const schema = { roles: ['worker', 'reviewer'], access: ['read-only', 'workspace-write', 'full'], objectives: ['cost', 'speed'], billing_keys: ['codex'], billing_modes: ['chatgpt-plan'], harness_keys: ['codex'], notify: ['finish'], jev_uses: ['auto', 'off'], host: 'codex', supported_range: '1.5.x' }
let current: ReturnType<typeof profile>

const api = (c: Call) => {
  if (c.path === `${base}/model-profiles/${MPF}`) return { body: current }
  if (c.path === `${base}/model-profiles/${MPF}/draft/issues`) return { body: { draft_hash: current.draft_hash, issues: [], independence: { status: 'independent', shared: [], unmatched: [] } } }
  if (c.path === `${base}/model-profiles/${MPF}/versions`) return { body: { items: [{ id: 'mpv_1', number: 1, state: 'published', catherd_name: 'o-team-v1', created_at: NOW, published_at: NOW, profile_id: MPF }], next_cursor: null } }
  if (c.path === `${base}/model-profiles/${MPF}/diff`) return { body: { from: 'latest', to: 'draft', changes: [{ path: ['roles', 'worker', 'rungs', '0'], path_display: 'roles.worker.rungs[0]', kind: 'changed', before: 'a', after: 'b' }] } }
  if (c.path === `${base}/catherd/status`) return { body: { available: true, supported: true, version: '1.5.0', home_display: '~', bin: '/x/catherd' } }
  if (c.path === `${base}/catherd/schema`) return { body: schema }
  if (c.path === `${base}/catherd/catalog`) return { body: catalog }
  return undefined
}
const editor = () => renderRoute(<ProfileEditorPage />, `/orgs/${ORG}/models/profiles/${MPF}`, '/orgs/:orgId/models/profiles/:profileId')

beforeEach(() => {
  applyLocale('en')
  current = profile()
  stubApi(api)
})
afterEach(() => {
  vi.unstubAllGlobals()
  applyLocale('en')
})

describe('profile editor', () => {
  it('lists an unusable rung with its reason and does not let it be added', async () => {
    editor()
    await userEvent.click(await screen.findByRole('button', { name: /Worker/ }))
    await userEvent.click(screen.getByRole('button', { name: 'Add model' }))
    const dialog = await screen.findByRole('dialog')
    const row = (await within(dialog).findByText('codex:gpt-6-sol#low')).closest('li') as HTMLElement
    expect(within(row).getByRole('button', { name: 'Add' })).toBeDisabled()
    expect(within(row).getByText(/Not offered by this account\./)).toBeInTheDocument()
    const ok = within(dialog).getByText('codex:gpt-6-sol#high').closest('li') as HTMLElement
    expect(within(ok).getByRole('button', { name: 'Add' })).toBeEnabled()
  })

  it('marks the validation stale and disables Publish after an edit', async () => {
    editor()
    const publish = await screen.findByRole('button', { name: 'Publish' })
    await waitFor(() => expect(publish).toBeEnabled())
    expect(screen.queryByText('Out of date')).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: /Worker/ }))
    await userEvent.click(screen.getByRole('radio', { name: 'Full' }))
    expect(await screen.findByText('Out of date')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Publish' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Validate' })).toBeDisabled() // save first
  })

  it('renders diff paths left to right', async () => {
    editor()
    await userEvent.click(await screen.findByRole('button', { name: 'Preview diff' }))
    const path = await screen.findByText('roles.worker.rungs[0]')
    expect(path.closest('bdi')).toHaveAttribute('dir', 'ltr')
  })

  it('blocks saving while the advanced JSON does not parse', async () => {
    editor()
    await userEvent.click(await screen.findByRole('tab', { name: 'Advanced' }))
    const box = await screen.findByLabelText('Profile JSON')
    await userEvent.clear(box)
    await userEvent.type(box, '{{ not json')
    expect(await screen.findByText(/Cannot save:/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save draft' })).toBeDisabled()
    expect(screen.getByText('Fix the JSON to save.')).toBeInTheDocument()
  })

  it('embedded mode has no page title', async () => {
    renderRoute(<ProfileEditorPage embedded profileId={MPF} />, `/orgs/${ORG}/x`, '/orgs/:orgId/x')
    await screen.findByRole('button', { name: 'Save draft' })
    expect(screen.queryByRole('heading', { level: 1 })).toBeNull()
  })
})

describe('independence badge', () => {
  it.each([
    ['independent', 'Independent reviewer'],
    ['same_model', 'Reviewer uses the same model'],
    ['unknown', 'Independence unknown'],
    ['reviewer_disabled', 'Reviewer disabled'],
  ] as const)('says %s in words', (status, text) => {
    render(<TooltipProvider><IndependenceBadge independence={{ status, shared: [], unmatched: [] }} /></TooltipProvider>)
    expect(screen.getByText(text)).toBeInTheDocument()
  })
})

describe('versions built for another host', () => {
  it('labels the rollback of a stale-host version "Republish for current host"', async () => {
    stubApi((c) => {
      if (c.path === `${base}/catherd/status`) return { body: { available: true, supported: true, version: '1.5.0', home_display: '~', bin: '/x', host: 'claude-code' } }
      if (c.path === `${base}/model-profiles/${MPF}/versions`) return { body: { items: [{ id: 'mpv_1', number: 1, state: 'published', host: 'codex', catherd_name: 'o-team-v1', created_at: NOW, published_at: NOW, profile_id: MPF }], next_cursor: null } }
      return api(c)
    })
    editor()
    expect(await screen.findByText('Built for codex')).toBeInTheDocument()
    await userEvent.click(await screen.findByRole('button', { name: /Republish for current host/ }))
    expect(await screen.findByText(/Republish version 1 for claude-code/)).toBeInTheDocument()
  })
})
