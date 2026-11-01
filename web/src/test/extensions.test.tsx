import { cleanup, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { applyLocale } from '@/i18n/direction'
import { ExtensionsPage } from '@/features/extensions/extensions-page'
import { NOW, ORG, renderRoute, stubApi, type Call } from './helpers'

const base = `/api/v1/orgs/${ORG}`
const manifest = (id: string, over: Record<string, unknown> = {}) => ({
  id, version: '1', source: 'builtin', kind: 'code_graph', integration: 'integrated', transport: 'cli',
  license: { spdx: 'MIT', gate: 'none', notice_key: `n.${id}` }, runtime: {}, config_schema: null, credential_fields: [],
  capabilities: [], read_tools: [], write_tools: [], supported_hosts: [], requires: {}, ...over,
})
const detail = (id: string, over: Record<string, unknown> = {}) => ({
  manifest: manifest(id), state: 'absent', usable: { ok: false, reason: 'not_registered' }, health: null, revision: 3, path: null,
  detected: null, last_test: null, registered_at: null, last_error: null, disabled_reason: null, license_state: null, license_notices: [],
  config: {}, installs: [], credentials: [], project_bindings: [], install_guidance: { text_key: `extensions.install.${id}.text`, commands: [] }, ...over,
})
const NOTICE = { locale: 'en', text: 'Noncommercial notice text.', sha256: 'abc123' }
const details: Record<string, ReturnType<typeof detail>> = {}
let calls: Call[]

const api = (c: Call) => {
  if (c.path === `${base}/extensions`) return { body: { items: Object.values(details).map(({ manifest: m, state, usable, health, revision }) => ({ manifest: m, state, usable, health, revision })) } }
  const m = c.path.match(/\/extensions\/([^/]+)$/)
  if (m && c.method === 'GET') return { body: details[m[1]] }
  if (c.path.endsWith('/license') && c.method === 'PUT') return { body: details.gitnexus }
  if (c.path.includes('/credentials/') && c.method === 'PUT') return { body: {} }
  if (c.path.endsWith('/detect')) return { status: 202, body: { id: 'op_1', status: 'running' } }
  if (c.path === `${base}/projects` && c.method === 'GET') return { body: { items: [{ id: 'prj_1', name: 'Shop', state: 'active', revision: 1 }], next_cursor: null } }
  if (c.method === 'PUT' && c.path.includes('/projects/prj_1/extensions/')) return { body: { project_id: 'prj_1', extension_id: 'strix', enabled: true, revision: 1 } }
  return undefined
}
const page = () => renderRoute(<ExtensionsPage />, `/orgs/${ORG}/extensions`, '/orgs/:orgId/extensions')
const open = async (name: RegExp | string) => {
  const card = (await screen.findByRole('heading', { name })).closest('li') as HTMLElement
  await userEvent.click(within(card).getByRole('button', { name: 'Manage' }))
  return screen.findByTestId('extension-sheet')
}

beforeEach(() => {
  applyLocale('en')
  for (const k of Object.keys(details)) delete details[k]
  details.gitnexus = detail('gitnexus', {
    manifest: manifest('gitnexus', { license: { spdx: 'PolyForm-Noncommercial-1.0.0', gate: 'acceptance_required', notice_key: 'n' } }),
    state: 'license_required', path: '/opt/gitnexus', license_notices: [NOTICE],
  })
  details['openclaw-wiki'] = detail('openclaw-wiki', { manifest: manifest('openclaw-wiki', { integration: 'detect_only' }), state: 'tested', path: '/opt/wiki', detected: { version: '1', sha256: 'f'.repeat(64), at: NOW } })
  details.strix = detail('strix', {
    manifest: manifest('strix', { credential_fields: [{ field: 'llm_api_key', label_key: 'extensions.credential.strix.llm_api_key', required: true }] }),
    state: 'registered', path: '/opt/strix', credentials: [{ field: 'llm_api_key', set: true }],
  })
  calls = stubApi(api)
})
afterEach(() => {
  vi.unstubAllGlobals()
  applyLocale('en')
})

describe('extensions page', () => {
  it('disables Detect with the reason until the license is accepted', async () => {
    page()
    const sheet = await open('GitNexus')
    expect(await within(sheet).findByTestId('detect-blocked')).toHaveTextContent('license is accepted')
    expect(within(sheet).getByRole('button', { name: 'Detect' })).toBeDisabled()
  })

  it('accepts the license only with a basis, a note and the exact extension id, sending the notice digest', async () => {
    page()
    const sheet = await open('GitNexus')
    await userEvent.click(await within(sheet).findByRole('button', { name: 'Review and accept' }))
    const dialog = await screen.findByRole('dialog', { name: /License for GitNexus/ })
    const accept = within(dialog).getByRole('button', { name: 'Accept license' })
    expect(within(dialog).getByTestId('license-notice')).toHaveTextContent('Noncommercial notice text.')
    expect(accept).toBeDisabled()
    await userEvent.click(within(dialog).getByLabelText('My use is noncommercial'))
    await userEvent.type(within(dialog).getByLabelText('Note'), 'research')
    await userEvent.type(within(dialog).getByLabelText(/Type gitnexus to confirm/), 'gitnex')
    expect(accept).toBeDisabled()
    await userEvent.type(within(dialog).getByLabelText(/Type gitnexus to confirm/), 'us')
    expect(accept).toBeEnabled()
    await userEvent.click(accept)
    await waitFor(() => expect(calls.some((c) => c.method === 'PUT' && c.path.endsWith('/extensions/gitnexus/license'))).toBe(true))
    const put = calls.find((c) => c.method === 'PUT' && c.path.endsWith('/license'))!
    expect(put.body).toEqual({ basis: 'noncommercial_use', note: 'research', confirm_phrase: 'gitnexus', notice_sha256: 'abc123' })
    expect(put.headers.get('If-Match')).toBe('"3"')
  })

  it('offers no Enable for a detect-only entry', async () => {
    page()
    const sheet = await open(/OpenClaw Wiki/)
    expect(await within(sheet).findByText(/detect-only/)).toBeInTheDocument()
    expect(within(sheet).queryByRole('button', { name: 'Enable' })).not.toBeInTheDocument()
    expect(within(sheet).getByRole('button', { name: 'Test' })).toBeEnabled()
  })

  it('never prefills a credential and empties the input after saving', async () => {
    page()
    const sheet = await open('Strix')
    const input = (await within(sheet).findByLabelText('LLM API key')) as HTMLInputElement
    expect(input.value).toBe('')
    expect(input.type).toBe('password')
    expect(within(sheet).getByText('Set')).toBeInTheDocument()
    await userEvent.type(input, 'test-value-1')
    await userEvent.click(within(sheet).getByRole('button', { name: 'Replace' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'PUT' && c.path.endsWith('/credentials/llm_api_key'))).toBe(true))
    expect(calls.find((c) => c.path.endsWith('/credentials/llm_api_key'))!.body).toEqual({ value: 'test-value-1' })
    await waitFor(() => expect(input.value).toBe(''))
  })

  it('enables a tested extension although the server reports it unusable only because it is disabled', async () => {
    details.strix = detail('strix', { manifest: manifest('strix'), state: 'tested', usable: { ok: false, reason: 'disabled' }, path: '/opt/strix' })
    page()
    const sheet = await open('Strix')
    expect(await within(sheet).findByRole('button', { name: 'Enable' })).toBeEnabled()
  })
  it('keeps Enable off when the extension is unusable for another reason', async () => {
    details.strix = detail('strix', { manifest: manifest('strix'), state: 'tested', usable: { ok: false, reason: 'binary_changed' }, path: '/opt/strix' })
    page()
    const sheet = await open('Strix')
    expect(await within(sheet).findByRole('button', { name: 'Enable' })).toBeDisabled()
  })
  it('binds a project with the binding revision (0 before it exists) as If-Match', async () => {
    details.strix = { ...details.strix, state: 'enabled', usable: { ok: true, reason: '' } }
    page()
    const sheet = await open('Strix')
    await userEvent.click(await within(sheet).findByRole('switch', { name: 'Use for Shop' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'PUT' && c.path.endsWith('/projects/prj_1/extensions/strix'))).toBe(true))
    const put = calls.find((c) => c.method === 'PUT' && c.path.includes('/projects/'))!
    expect(put.headers.get('If-Match')).toBe('"0"')
    expect(put.body).toEqual({ enabled: true })
  })

  it('renders the sheet on the end side and mirrors it in Persian', async () => {
    applyLocale('fa')
    page()
    const heading = await screen.findByRole('heading', { name: 'Strix' })
    await userEvent.click(within(heading.closest('li') as HTMLElement).getByRole('button', { name: 'مدیریت' }))
    const sheet = await screen.findByTestId('extension-sheet')
    expect(sheet.className).toMatch(/\bend-0\b/)
    expect(sheet.className).not.toMatch(/\b(right|left)-0\b/)
    expect(document.documentElement.dir).toBe('rtl')
  })

  it('shows a translated reason, never the raw code, for a revoked license in both languages', async () => {
    details.gitnexus = detail('gitnexus', { manifest: manifest('gitnexus'), state: 'license_required', usable: { ok: false, reason: 'license_revoked' }, path: '/opt/gitnexus' })
    page()
    expect(await screen.findByText('The license acceptance was revoked')).toBeInTheDocument()
    expect(screen.queryByText('license_revoked')).not.toBeInTheDocument()
    cleanup()
    applyLocale('fa')
    page()
    expect(await screen.findByText('پذیرش مجوز لغو شده است')).toBeInTheDocument()
    expect(screen.queryByText('license_revoked')).not.toBeInTheDocument()
  })
})
