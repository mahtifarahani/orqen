import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { SystemCheckPanel } from '@/features/setup/system-check-panel'
import { applyLocale } from '@/i18n/direction'
import { NOW, ORG, renderRoute, stubApi, type Call } from './helpers'

const base = `/api/v1/orgs/${ORG}`
const check = (id: string, status: string, extra: Record<string, unknown> = {}) => ({ id, group: 'binaries', status, detail: '', fix_code: '', fix_command: [], required: false, blocks: [], duration_ms: 1, ...extra })
const checks = [
  check('bin.bun', 'fail', { required: true, detail: 'bun was not found', fix_code: 'bun_missing', fix_command: ['brew', 'install', 'oven-sh/bun/bun'], blocks: ['profile_publish'] }),
  check('bin.git', 'pass'),
  check('bin.node', 'warn', { fix_code: 'node_too_old' }),
  check('launchd.start_at_login', 'skipped', { group: 'service' }),
]
let report: { checked_at: string | null; checks: unknown[] }
let optedIn: boolean

const api = (c: Call) => {
  if (c.path === `${base}/system/preflight` && c.method === 'GET') return { body: report }
  if (c.path === `${base}/system/preflight` && c.method === 'POST') return { status: 202, body: { id: 'op_1', kind: 'system_preflight', status: 'pending', subject: { kind: 'org', id: ORG }, revision: 1, created_at: NOW, updated_at: NOW } }
  if (c.path === `${base}/operations/op_1`) return { body: { id: 'op_1', kind: 'system_preflight', status: 'running', subject: { kind: 'org', id: ORG }, revision: 1, created_at: NOW, updated_at: NOW } }
  if (c.path === `${base}/system/launchd` && c.method === 'GET') return { body: { opted_in: optedIn, plist_path: '~/Library/LaunchAgents/dev.orqen.server.plist', plist_xml: '<plist/>', install_commands: ['cp a b', 'launchctl bootstrap gui/$(id -u) b'], uninstall_commands: ['launchctl bootout x'], revision: 0 } }
  if (c.path === `${base}/system/launchd` && c.method === 'PATCH') return { body: {} }
  return undefined
}
const view = (embedded = false) => renderRoute(<SystemCheckPanel orgId={ORG} embedded={embedded} />, '/x', '/x')
const DOCUMENTED = [`${base}/system/preflight`, `${base}/system/launchd`, `${base}/operations/op_1`, `${base}/system/keychain-probe`]

beforeEach(() => {
  applyLocale('en')
  report = { checked_at: NOW, checks }
  optedIn = false
  Object.assign(navigator, { clipboard: { writeText: vi.fn().mockResolvedValue(undefined) } })
})
afterEach(() => {
  vi.unstubAllGlobals()
  applyLocale('en')
})

describe('system check panel', () => {
  it('shows pass, warn, fail and skipped as words, with fix hints for failures', async () => {
    stubApi(api)
    view()
    const bun = (await screen.findByText('bin.bun')).closest('li') as HTMLElement
    expect(within(bun).getByText('Fail')).toBeInTheDocument()
    expect(within(bun).getByText('Required')).toBeInTheDocument()
    expect(within(bun).getByText(/bun was not found on the PATH Orqen uses/)).toBeInTheDocument()
    expect(within(bun).getByText('brew install oven-sh/bun/bun').closest('bdi')).toHaveAttribute('dir', 'ltr')
    expect(within((screen.getByText('bin.git')).closest('li') as HTMLElement).getByText('Pass')).toBeInTheDocument()
    expect(within((screen.getByText('bin.node')).closest('li') as HTMLElement).getByText('Warning')).toBeInTheDocument()
    expect(within((screen.getByText('launchd.start_at_login')).closest('li') as HTMLElement).getByText('Skipped')).toBeInTheDocument()
  })

  it('Copy only writes to the clipboard and calls no endpoint', async () => {
    const calls = stubApi(api)
    view()
    await userEvent.click(await screen.findByRole('button', { name: /Copy: brew install/ }))
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith('brew install oven-sh/bun/bun')
    expect(calls.every((c) => c.method === 'GET')).toBe(true)
  })

  it('Re-check starts one new operation with an Idempotency-Key', async () => {
    const calls = stubApi(api)
    view()
    await userEvent.click(await screen.findByRole('button', { name: 'Re-check' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true))
    const post = calls.find((c) => c.method === 'POST')!
    expect(post.path).toBe(`${base}/system/preflight`)
    expect(post.headers.get('Idempotency-Key')).toBeTruthy()
  })

  it('on wizard entry with no report it starts one check by itself', async () => {
    report = { checked_at: null, checks: [] }
    const calls = stubApi(api)
    view(true)
    await waitFor(() => expect(calls.filter((c) => c.method === 'POST')).toHaveLength(1))
  })

  it('launchd shows commands only and never calls anything but the documented endpoints', async () => {
    optedIn = true
    const calls = stubApi(api)
    view()
    expect(await screen.findByText('cp a b')).toBeInTheDocument()
    expect(screen.getByText('launchctl bootout x')).toBeInTheDocument()
    expect(screen.getByText('Orqen only displays these commands.')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: /Copy: cp a b/ }))
    for (const c of calls) expect(DOCUMENTED).toContain(c.path)
    expect(calls.filter((c) => c.method !== 'GET')).toHaveLength(0)
  })
})
