import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { applyLocale } from '@/i18n/direction'
import { ProjectDetailPage } from '@/features/projects/project-detail-page'
import { NOW, ORG, problem, renderRoute, stubApi, type Call } from './helpers'

const PRJ = 'prj_A'
const base = `/api/v1/orgs/${ORG}/projects/${PRJ}`
const repo = {
  connection_id: 'con_G', provider_project_id: '7', role: 'primary', target_branch: 'main', path_snapshot: 'group/app', default_branch_snapshot: 'main', package_path: null, note: null,
}
const projectDto = (over: Record<string, unknown> = {}) => ({
  id: PRJ, name: 'My App', description: null, state: 'draft', blocker_count: 2, automation_enabled: false, revision: 3, created_at: NOW, updated_at: NOW,
  mapping: { repos: [repo], jira: null, confluence: [] }, mapping_hash: 'h', mapping_check: null, commands: [], protected_paths: [], profile_binding: null,
  latest_contract_snapshot_id: null, reviewed_contract_snapshot_id: null, enabled_at: null, enabled_policy_version_id: null, enabled_profile_version_id: null, ...over,
})
let readiness: { ready: boolean; blockers: { code: string; detail: string; tab: string }[] }
let project: ReturnType<typeof projectDto>
let enableResult: ReturnType<typeof problem> | { status: number; body: unknown }

const api = (c: Call) => {
  if (c.path === base && c.method === 'GET') return { body: project }
  if (c.path === `${base}/readiness`) return { body: { ...readiness, state: 'draft', warnings: [], evaluated_at: NOW } }
  if (c.path === `${base}/automation/enable`) return enableResult
  if (c.path === `${base}/mapping` && c.method === 'PUT') return problem(409, 'revision_conflict', { current: projectDto({ revision: 4, name: 'My App' }) })
  if (c.path === `${base}/commands` && c.method === 'PUT') return { body: projectDto({ revision: 4 }) }
  if (c.path === `/api/v1/orgs/${ORG}/connections`) return { body: { items: [], next_cursor: null } }
  if (c.path.endsWith('/branches') || c.path.includes('/lookup/')) return { body: { items: [] } }
  if (c.path.startsWith(`/api/v1/orgs/${ORG}/model-profiles`)) return { body: { items: [], next_cursor: null } }
  return undefined
}
const render = (props: Record<string, unknown> = {}, search = '') => renderRoute(<ProjectDetailPage {...props} />, `/orgs/${ORG}/projects/${PRJ}${search}`, '/orgs/:orgId/projects/:projectId')

beforeEach(() => {
  applyLocale('en')
  project = projectDto()
  readiness = { ready: false, blockers: [{ code: 'jira_mapping_missing', detail: 'x', tab: 'mapping' }, { code: 'test_command_missing', detail: 'x', tab: 'commands' }] }
  enableResult = { status: 200, body: projectDto({ automation_enabled: true }) }
})
afterEach(() => {
  vi.unstubAllGlobals()
  applyLocale('en')
})

describe('project detail', () => {
  it('links each blocker to its tab', async () => {
    stubApi(api)
    render()
    const fix = await screen.findAllByRole('button', { name: /^Fix/ })
    expect(fix).toHaveLength(2)
    await userEvent.click(fix[1])
    expect(await screen.findByRole('tab', { name: 'Commands', selected: true })).toBeInTheDocument()
    expect(screen.getByText('No commands yet. Add one, or accept a suggestion from the repository contract.')).toBeInTheDocument()
  })

  it('disables Enable with the reasons while blockers remain', async () => {
    stubApi(api)
    render()
    const enable = await screen.findByRole('button', { name: 'Enable automation' })
    expect(enable).toBeDisabled()
    expect(enable).toHaveAccessibleDescription(/No Jira project is mapped\..*There is no approved test command\./)
  })

  it('shows the blockers when the server refuses with 409 project_not_ready', async () => {
    readiness = { ready: true, blockers: [] }
    enableResult = problem(409, 'project_not_ready', { errors: [{ field: 'catherd_doctor_missing', message: 'doctor' }] })
    const calls = stubApi(api)
    render()
    await userEvent.click(await screen.findByRole('button', { name: 'Enable automation' }))
    const alert = await screen.findByRole('alert')
    expect(within(alert).getByText('catherd doctor has not been run.')).toBeInTheDocument()
    const post = calls.find((c) => c.path.endsWith('/automation/enable'))
    expect(post?.headers.get('Idempotency-Key')).toBeTruthy()
    expect(post?.headers.get('If-Match')).toBe('"3"')
  })

  it('offers the conflict UI when saving the mapping loses the race', async () => {
    stubApi(api)
    render({}, '?tab=mapping')
    const target = await screen.findByLabelText('Target branch')
    await userEvent.clear(target)
    await userEvent.type(target, 'release')
    await userEvent.click(screen.getByRole('button', { name: 'Save mapping' }))
    expect(await screen.findByTestId('conflict-alert')).toBeInTheDocument()
    expect(screen.getByDisplayValue('release')).toBeInTheDocument() // the user's input stays
  })

  it('sends argv as separate items, never as one string', async () => {
    const calls = stubApi(api)
    render({}, '?tab=commands')
    await userEvent.click(await screen.findByRole('button', { name: 'Add command' }))
    await userEvent.type(screen.getByLabelText('Key'), 'unit')
    await userEvent.type(screen.getByLabelText('Argument 1'), 'pnpm')
    await userEvent.click(screen.getByRole('button', { name: 'Add argument' }))
    await userEvent.type(screen.getByLabelText('Argument 2'), 'test --run "a b"')
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'PUT' && c.path.endsWith('/commands'))).toBe(true))
    const put = calls.find((c) => c.method === 'PUT' && c.path.endsWith('/commands'))
    const sent = (put!.body as { commands: { argv: unknown }[] }).commands[0]
    expect(sent.argv).toEqual(['pnpm', 'test --run "a b"'])
    expect(Array.isArray(sent.argv)).toBe(true)
  })

  it('embedded mode renders without the page title or back link', async () => {
    stubApi(api)
    render({ embedded: true, projectId: PRJ })
    await screen.findByRole('tab', { name: 'Readiness' })
    expect(screen.queryByRole('heading', { level: 1 })).toBeNull()
    expect(screen.queryByRole('link', { name: /All projects/ })).toBeNull()
  })
})
