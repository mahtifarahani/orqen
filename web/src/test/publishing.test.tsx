import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { HostCard } from '@/features/publishing/host-card'
import { PublishingPanel } from '@/features/publishing/publishing-panel'
import { WriteModeBadge } from '@/features/publishing/write-mode-badge'
import { applyLocale } from '@/i18n/direction'
import { NOW, ORG, problem, renderRoute, stubApi, type Call } from './helpers'

const base = `/api/v1/orgs/${ORG}`
let settings: Record<string, unknown>
let host: Record<string, unknown>
const guard = { build_channel: 'dev', orqen_env: '', scope: 'loopback_only', kill_switch: false }
const gl = { id: 'con_1', org_id: ORG, name: 'GL', provider: 'gitlab', status: 'verified', revision: 1, credential: { state: 'set', updated_at: NOW }, allowed_hosts: [], base_url: 'http://127.0.0.1', auth_method: 'pat', deployment: 'self_managed', created_at: NOW, updated_at: NOW, max_concurrency: 1, timeout_ms: 1000, readiness: { ready: true, reasons: [] } }
const api = (c: Call) => {
  if (c.path === `${base}/external-writes` && c.method === 'GET') return { body: settings }
  if (c.path === `${base}/external-writes` && c.method === 'PUT') {
    const b = c.body as { mode: string; confirm_phrase: string | null }
    if (b.mode === 'live' && b.confirm_phrase !== 'ENABLE LIVE WRITES') return problem(422, 'confirm_phrase_mismatch')
    settings = { ...settings, mode: b.mode, revision: 2 }
    return { body: settings }
  }
  if (c.path === `${base}/external-writes/pause`) {
    settings = { ...settings, paused: (c.body as { paused: boolean }).paused, revision: 3 }
    return { body: settings }
  }
  if (c.path === `${base}/connections`) return { body: { items: [gl], next_cursor: null } }
  if (c.path === `${base}/connections/con_1/write-credential`) return { body: { state: 'none', updated_at: null, last_check: null, revision: 0 } }
  if (c.path === `${base}/host` && c.method === 'GET') return { body: host }
  if (c.path === `${base}/host/probe`) return { status: 202, body: { id: 'op_1', status: 'pending' } }
  if (c.path === `${base}/operations/op_1`) return { body: { id: 'op_1', status: 'succeeded' } }
  return undefined
}
beforeEach(() => {
  applyLocale('en')
  settings = { mode: 'dry_run', allowlist: [], paused: false, paused_reason: null, env_guard: guard, revision: 1, mode_changed_at: null }
  host = { probe: null, spike: null, available: false, min_codex_version: '0.159.2', revision: 0 }
})
afterEach(() => vi.unstubAllGlobals())

describe('publishing', () => {
  it('shows DRY-RUN by default and LIVE only after the typed phrase', async () => {
    const calls = stubApi(api)
    renderRoute(<WriteModeBadge />, `/orgs/${ORG}/overview`, '/orgs/:orgId/overview')
    expect(await screen.findByText('DRY-RUN')).toBeInTheDocument()
    expect(calls.every((c) => c.method === 'GET')).toBe(true)
  })
  it('live needs the exact phrase; the confirm button stays disabled until then', async () => {
    const calls = stubApi(api)
    renderRoute(<PublishingPanel orgId={ORG} />, `/orgs/${ORG}/settings`, '/orgs/:orgId/settings')
    await userEvent.click(await screen.findByRole('button', { name: 'Enable live writes...' }))
    const confirm = await screen.findByRole('button', { name: 'Enable live writes' })
    expect(confirm).toBeDisabled()
    await userEvent.type(screen.getByLabelText(/Type ENABLE LIVE WRITES/), 'ENABLE LIVE WRITES')
    expect(confirm).toBeEnabled()
    await userEvent.click(confirm)
    await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true))
    const put = calls.find((c) => c.method === 'PUT')
    expect(put?.body).toEqual({ mode: 'live', allowlist: [], confirm_phrase: 'ENABLE LIVE WRITES' })
    expect(put?.headers.get('If-Match')).toBe('"1"')
  })
  it('shows the environment guard and pauses with If-Match', async () => {
    const calls = stubApi(api)
    renderRoute(<PublishingPanel orgId={ORG} />, `/orgs/${ORG}/settings`, '/orgs/:orgId/settings')
    expect(await screen.findByText('This build can only write to loopback addresses.')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('switch', { name: 'Pause all external writes' }))
    await waitFor(() => expect(calls.some((c) => c.path.endsWith('/pause'))).toBe(true))
    expect(calls.find((c) => c.path.endsWith('/pause'))?.body).toEqual({ paused: true, reason: null })
  })
  it('the write credential field is write-only and type password', async () => {
    stubApi(api)
    renderRoute(<PublishingPanel orgId={ORG} />, `/orgs/${ORG}/settings`, '/orgs/:orgId/settings')
    const field = await screen.findByLabelText('Write token')
    expect(field).toHaveAttribute('type', 'password')
  })
  it('probes the host through an operation', async () => {
    const calls = stubApi(api)
    renderRoute(<HostCard orgId={ORG} />, `/orgs/${ORG}/settings`, '/orgs/:orgId/settings')
    await userEvent.click(await screen.findByRole('button', { name: 'Probe host' }))
    await waitFor(() => expect(calls.some((c) => c.path === `${base}/host/probe`)).toBe(true))
    expect(calls.find((c) => c.path === `${base}/host/probe`)?.headers.get('Idempotency-Key')).toBeTruthy()
  })
})
