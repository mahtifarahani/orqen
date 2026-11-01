import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { applyLocale } from '@/i18n/direction'
import { SkillsTab } from '@/features/skills/skills-tab'
import { RunSkillsManifest } from '@/features/skills/run-skills-manifest'
import { NOW, ORG, renderRoute, stubApi, type Call } from './helpers'

const base = `/api/v1/orgs/${ORG}`
const skill = (over: Record<string, unknown> = {}) => ({
  id: 'skl_1', slug: 'careful-edit', origin: 'imported', title: 'Careful edit', description: 'Edit small.', roles: ['worker'], when: 'always',
  semver: '1.0.0', current_version_id: 'skv_1', approved_version_id: null, revision: 4, updated_at: NOW, ...over,
})
const version = (over: Record<string, unknown> = {}) => ({
  id: 'skv_1', skill_id: 'skl_1', number: 1, semver: '1.0.0', name: 'careful-edit', description: 'Edit small.', roles: ['worker'], when: 'always',
  license: 'MIT', source: 'import', body: 'Do small edits.', content_sha256: 'a'.repeat(64), lint: { flags: [], warnings: [] }, state: 'draft',
  acknowledged_flags: [], approved_at: null, created_at: NOW, ...over,
})
let current: { skill: ReturnType<typeof skill>; versions: ReturnType<typeof version>[] }
let calls: Call[]
let bindings: { scope_key: string; bindings: unknown[]; revision: number }

const api = (c: Call) => {
  if (c.path === `${base}/skills`) return { body: { items: [current.skill] } }
  if (c.path === `${base}/skills/skl_1`) return { body: { ...current, update_available: false } }
  if (c.path === `${base}/skill-bindings` && c.method === 'GET') return { body: bindings }
  if (c.path === `${base}/skill-bindings` && c.method === 'PUT') return { body: { ...bindings, revision: 2 } }
  if (c.path.endsWith('/approve')) return { body: current.skill }
  if (c.path.endsWith('/skills/import')) return { status: 201, body: version() }
  return undefined
}
const open = async () => {
  renderRoute(<SkillsTab orgId={ORG} />, `/orgs/${ORG}/models`, '/orgs/:orgId/models')
  await userEvent.click(await screen.findByRole('button', { name: 'Review' }))
  return screen.findByTestId('skill-sheet')
}

beforeEach(() => {
  applyLocale('en')
  current = { skill: skill(), versions: [version()] }
  bindings = { scope_key: 'org', bindings: [], revision: 1 }
  calls = stubApi(api)
})
afterEach(() => {
  vi.unstubAllGlobals()
  applyLocale('en')
})

describe('skills tab', () => {
  it('approves a clean draft with the skill revision as If-Match', async () => {
    const sheet = await open()
    await userEvent.click(await within(sheet).findByRole('button', { name: 'Approve version' }))
    await waitFor(() => expect(calls.some((c) => c.path.endsWith('/skill-versions/skv_1/approve'))).toBe(true))
    const c = calls.find((x) => x.path.endsWith('/approve'))!
    expect(c.headers.get('If-Match')).toBe('"4"')
    expect(c.body).toEqual({ acknowledged_flags: [] })
  })

  it('needs every non-blocking flag acknowledged before approving', async () => {
    current.versions = [version({ lint: { flags: [{ code: 'network_fetch', line: 3, blocking: false }], warnings: [] } })]
    const sheet = await open()
    const approve = await within(sheet).findByRole('button', { name: 'Approve version' })
    expect(approve).toBeDisabled()
    await userEvent.click(within(sheet).getByRole('checkbox'))
    expect(approve).toBeEnabled()
    await userEvent.click(approve)
    await waitFor(() => expect(calls.find((x) => x.path.endsWith('/approve'))?.body).toEqual({ acknowledged_flags: ['network_fetch'] }))
  })

  it('never lets a blocking lint finding be approved', async () => {
    current.versions = [version({ lint: { flags: [{ code: 'secret_like', line: 1, blocking: true }], warnings: [] } })]
    const sheet = await open()
    expect(await within(sheet).findByRole('button', { name: 'Approve version' })).toBeDisabled()
    expect(within(sheet).getByText(/blocking lint|blocking finding/i)).toBeInTheDocument()
  })

  it('saves the bindings of the org scope with the set revision', async () => {
    current = { skill: skill({ approved_version_id: 'skv_1' }), versions: [version({ state: 'approved' })] }
    renderRoute(<SkillsTab orgId={ORG} />, `/orgs/${ORG}/models`, '/orgs/:orgId/models')
    await userEvent.selectOptions(await screen.findByLabelText('Skill'), 'skl_1')
    await userEvent.click(screen.getByRole('button', { name: 'Add' }))
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'PUT' && c.path === `${base}/skill-bindings`)).toBe(true))
    const put = calls.find((c) => c.method === 'PUT')!
    expect(put.headers.get('If-Match')).toBe('"1"')
    expect(put.body).toEqual({ scope_key: 'org', bindings: [{ role: 'worker', skill_id: 'skl_1', version_id: 'skv_1', enabled: true, position: 0 }] })
  })
})

describe('run skills manifest', () => {
  it('lists pinned and excluded skills per role', async () => {
    stubApi((c) => c.path === `${base}/runs/run_1/skills`
      ? { body: { fingerprint: 'f'.repeat(64), roles: { worker: { round0: [{ skill_id: 'a', version_id: 'v', slug: 'careful-edit', semver: '1.0.0', sha256: 'x' }], fix: [], excluded0: [{ slug: 'big', reason: 'skill_budget' }], excluded_fix: [] } } } }
      : undefined)
    renderRoute(<RunSkillsManifest orgId={ORG} runId="run_1" />, '/x', '/x')
    expect(await screen.findByText('Skills pinned for this run')).toBeInTheDocument()
    expect(screen.getByText(/careful-edit/)).toBeInTheDocument()
    expect(screen.getByText(/over the size budget/)).toBeInTheDocument()
  })
})

describe('import dialog', () => {
  it('sends the file name and text with an idempotency key and opens the new skill from the draft version', async () => {
    current = { skill: skill(), versions: [version()] }
    renderRoute(<SkillsTab orgId={ORG} />, `/orgs/${ORG}/models`, '/orgs/:orgId/models')
    await userEvent.click(await screen.findByRole('button', { name: 'Import skill' }))
    const dialog = await screen.findByRole('dialog', { name: 'Import a skill' })
    await userEvent.type(within(dialog).getByLabelText('Skill text'), 'hello')
    await userEvent.click(within(dialog).getByRole('button', { name: 'Import' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.path.endsWith('/skills/import'))).toBe(true))
    const c = calls.find((x) => x.path.endsWith('/skills/import'))!
    expect(c.body).toEqual({ filename: 'SKILL.md', content: 'hello' })
    expect(c.headers.get('Idempotency-Key')).toBeTruthy()
    expect(await screen.findByTestId('skill-sheet')).toBeInTheDocument()
  })
})
