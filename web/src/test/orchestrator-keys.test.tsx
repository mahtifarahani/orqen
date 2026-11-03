import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useLocation } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { OrchestratorKeysTab } from '@/features/models/orchestrator-keys-tab'
import { OrgPolicyPanel } from '@/features/policy/policy-panel'
import { applyLocale } from '@/i18n/direction'
import { NOW, ORG, problem, renderRoute, stubApi, type Call } from './helpers'

const base = `/api/v1/orgs/${ORG}`
const SENTINEL = 'orqen-SENTINEL-typed-value-123'
const kinds = [
  { kind: 'jev', env_name: 'TYPESAFE_API_KEY', used_by: ['doctor'], verifiable: true, required_reason: null },
  { kind: 'artificial_analysis', env_name: 'ARTIFICIAL_ANALYSIS_API_KEY', used_by: ['catalog_sync'], verifiable: true, required_reason: null },
  { kind: 'gemini', env_name: 'GEMINI_API_KEY', used_by: ['doctor', 'profile_publish'], verifiable: true, required_reason: 'isolated_harness' },
  { kind: 'cursor', env_name: 'CURSOR_API_KEY', used_by: ['doctor'], verifiable: false, required_reason: 'isolated_harness' },
  { kind: 'xai', env_name: 'XAI_API_KEY', used_by: ['doctor'], verifiable: true, required_reason: 'isolated_harness' },
]
let state: Record<string, { state: string; revision: number; last_check: unknown }>
const secretRows = () => kinds.map((k) => ({ ...k, state: state[k.kind]?.state ?? 'none', revision: state[k.kind]?.revision ?? 0, updated_at: state[k.kind]?.state === 'set' ? NOW : null, last_check: state[k.kind]?.last_check ?? null }))
const doctor = (host: string) => ({ operation_id: 'op_d', checked_at: NOW, ready: true, checks: [], artifact_id: 'art_1', host })
const hosts = (effective: string) => [
  { host: 'codex', effective: effective === 'codex', catherd_supported: true, orqen_host_adapter: 'available', last_doctor: doctor('codex') },
  { host: 'claude-code', effective: effective === 'claude-code', catherd_supported: true, orqen_host_adapter: 'unavailable', last_doctor: null },
]
let putFails = false
const api = (c: Call) => {
  if (c.path === `${base}/catherd/secrets` && c.method === 'GET') return { body: secretRows() }
  const m = /\/catherd\/secrets\/(\w+)$/.exec(c.path)
  if (m && c.method === 'PUT') {
    if (putFails) return problem(422, 'secret_invalid', { errors: [{ field: 'secret', message: 'must be a single line without control characters' }] })
    state[m[1]!] = { state: 'set', revision: (state[m[1]!]?.revision ?? 0) + 1, last_check: null }
    return { body: secretRows().find((r) => r.kind === m[1]) }
  }
  if (m && c.method === 'DELETE') {
    state[m[1]!] = { state: 'none', revision: (state[m[1]!]?.revision ?? 0) + 1, last_check: null }
    return { body: secretRows().find((r) => r.kind === m[1]) }
  }
  if (/\/catherd\/secrets\/\w+\/check$/.test(c.path)) return { status: 202, body: { id: 'op_check', kind: 'catherd_secret_check', status: 'pending', created_at: NOW, updated_at: NOW, subject: { kind: 'catherd_secret', id: 'jev' }, revision: 1, result: null, problem: null } }
  if (c.path === `${base}/operations/op_check`) return { body: { id: 'op_check', kind: 'catherd_secret_check', status: 'succeeded', created_at: NOW, updated_at: NOW, subject: { kind: 'catherd_secret', id: 'jev' }, revision: 3, result: { status: 'refused' }, problem: null } }
  if (c.path === `${base}/catherd/hosts`) return { body: hosts('codex') }
  if (c.path === `${base}/catherd/status`) return { body: { available: true, supported: true, version: '1.5.0', home_display: '~', bin: '/x/catherd', host: 'codex' } }
  if (c.path === `${base}/policy/effective`) return { body: { hash: 'h', items: [{ key: 'orchestrator.host', value: 'codex', default: 'codex', locked: false, source: { layer: 'org', number: 2, version_id: 'pol_2' } }] } }
  return undefined
}
function Where() {
  const l = useLocation()
  return <output data-testid="where">{l.pathname + l.search}</output>
}
const tab = () => renderRoute(<><OrchestratorKeysTab orgId={ORG} /><Where /></>, `/orgs/${ORG}/models`, '/orgs/:orgId/*')

beforeEach(() => {
  applyLocale('en')
  state = {}
  putFails = false
})
afterEach(() => {
  vi.unstubAllGlobals()
  applyLocale('en')
})

describe('keys table', () => {
  it('lists every key with its environment name left-to-right and the privacy note', async () => {
    stubApi(api)
    tab()
    const row = (await screen.findByText('TYPESAFE_API_KEY')).closest('tr') as HTMLElement
    expect(within(row).getByText('Not set')).toBeInTheDocument()
    expect(screen.getByText('TYPESAFE_API_KEY').closest('bdi')).toHaveAttribute('dir', 'ltr')
    expect(screen.getAllByRole('row')).toHaveLength(6)
    expect(screen.getByText(/never shown again|not read or changed/)).toBeInTheDocument()
    expect(within(screen.getByText('CURSOR_API_KEY').closest('tr') as HTMLElement).getByText(/cannot verify this key/)).toBeInTheDocument()
  })

  it('sends the typed key only in the PUT body and leaves it nowhere in the page', async () => {
    const calls = stubApi(api)
    tab()
    const row = (await screen.findByText('TYPESAFE_API_KEY')).closest('tr') as HTMLElement
    await userEvent.click(within(row).getByRole('button', { name: /^Set/ }))
    const input = await screen.findByLabelText('Key')
    expect(input).toHaveAttribute('type', 'password')
    expect(input).toHaveAttribute('autocomplete', 'off')
    await userEvent.type(input, SENTINEL)
    await userEvent.click(screen.getByRole('button', { name: 'Save key' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    // The request: If-Match "0" for a key that was never saved, the value in the body.
    const put = calls.find((c) => c.method === 'PUT')!
    expect(put.path).toBe(`${base}/catherd/secrets/jev`)
    expect(put.headers.get('If-Match')).toBe('"0"')
    expect(put.body).toEqual({ secret: SENTINEL })
    // No other request carries it, in the URL, headers or body.
    for (const c of calls.filter((x) => x !== put)) {
      expect(JSON.stringify([c.path, c.search.toString(), c.body, [...c.headers.entries()]])).not.toContain(SENTINEL)
    }
    // The page has no trace of it, and the row now shows a stored key.
    expect(document.body.innerHTML).not.toContain(SENTINEL)
    expect(document.body.textContent).not.toContain(SENTINEL)
    expect(screen.queryByDisplayValue(SENTINEL)).toBeNull()
    await waitFor(() => expect(within(screen.getByText('TYPESAFE_API_KEY').closest('tr') as HTMLElement).getByText(/^Set/)).toBeInTheDocument())
    // Replacing reopens an empty field.
    await userEvent.click(within(screen.getByText('TYPESAFE_API_KEY').closest('tr') as HTMLElement).getByRole('button', { name: /^Replace/ }))
    expect(await screen.findByLabelText('Key')).toHaveValue('')
  })

  it('clears the field when the server refuses the key', async () => {
    putFails = true
    stubApi(api)
    tab()
    const row = (await screen.findByText('XAI_API_KEY')).closest('tr') as HTMLElement
    await userEvent.click(within(row).getByRole('button', { name: /^Set/ }))
    await userEvent.type(await screen.findByLabelText('Key'), SENTINEL)
    await userEvent.click(screen.getByRole('button', { name: 'Save key' }))
    expect(await screen.findByText(/not valid/)).toBeInTheDocument()
    expect(screen.getByLabelText('Key')).toHaveValue('')
    expect(document.body.innerHTML).not.toContain(SENTINEL)
  })

  it('starts a check as an operation with an Idempotency-Key and no body, and removes with a confirmation', async () => {
    state.jev = { state: 'set', revision: 1, last_check: null }
    const calls = stubApi(api)
    tab()
    const row = (await screen.findByText('TYPESAFE_API_KEY')).closest('tr') as HTMLElement
    expect(within(row).getByRole('button', { name: /^Check/ })).toBeEnabled()
    expect(within(screen.getByText('GEMINI_API_KEY').closest('tr') as HTMLElement).getByRole('button', { name: /^Check/ })).toBeDisabled()
    await userEvent.click(within(row).getByRole('button', { name: /^Check/ }))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.path.endsWith('/secrets/jev/check'))).toBe(true))
    const check = calls.find((c) => c.method === 'POST')!
    expect(check.headers.get('Idempotency-Key')).toBeTruthy()
    expect(check.body).toBeUndefined()
    await userEvent.click(within(row).getByRole('button', { name: /^Remove/ }))
    expect(await screen.findByText(/Remove the Jev key\?/)).toBeInTheDocument()
    expect(calls.some((c) => c.method === 'DELETE')).toBe(false)
    await userEvent.click(screen.getByRole('button', { name: 'Remove key' }))
    await waitFor(() => expect(calls.find((c) => c.method === 'DELETE')?.headers.get('If-Match')).toBe('"1"'))
  })

  it('shows each check result as its own state word', async () => {
    const at = { checked_at: NOW, detail: 'HTTP 401', operation_id: 'op_1' }
    state = {
      jev: { state: 'set', revision: 2, last_check: { status: 'ok', ...at } },
      artificial_analysis: { state: 'set', revision: 2, last_check: { status: 'refused', ...at } },
      gemini: { state: 'set', revision: 2, last_check: { status: 'unchecked', ...at } },
      cursor: { state: 'set', revision: 2, last_check: { status: 'unverifiable', ...at } },
    }
    stubApi(api)
    tab()
    const word = async (env: string, text: string) => expect(within((await screen.findByText(env)).closest('tr') as HTMLElement).getByText(text)).toBeInTheDocument()
    await word('TYPESAFE_API_KEY', 'Valid')
    await word('ARTIFICIAL_ANALYSIS_API_KEY', 'Refused')
    await word('GEMINI_API_KEY', 'Could not check')
    await word('CURSOR_API_KEY', 'Cannot verify')
    await word('XAI_API_KEY', 'Not set')
  })
})

describe('orchestrator host card', () => {
  it('names the effective host with its policy source and shows the adapter notice on claude-code', async () => {
    stubApi(api)
    tab()
    const codex = (await screen.findByLabelText('codex')) as HTMLInputElement
    expect(codex.checked).toBe(true)
    expect(screen.getByText(/Organization/)).toBeInTheDocument()
    const cc = screen.getByLabelText('claude-code') as HTMLInputElement
    expect(cc.checked).toBe(false)
    const option = cc.closest('[data-host]') as HTMLElement
    expect(within(option).getByText(/no execution adapter for this host/)).toBeInTheDocument()
    expect(within(option).getByText('No Orqen adapter')).toBeInTheDocument()
    expect(screen.getAllByRole('button', { name: /Run doctor for this host/ })).toHaveLength(2)
  })

  it('routes a host change to the policy draft with the value prefilled; nothing is saved here', async () => {
    const calls = stubApi(api)
    tab()
    await userEvent.click(await screen.findByLabelText('claude-code'))
    expect(await screen.findByText(/Change the host to claude-code\?/)).toBeInTheDocument()
    expect(screen.getAllByText(/no execution adapter for this host/).length).toBeGreaterThan(1)
    expect(calls.some((c) => c.method !== 'GET')).toBe(false)
    await userEvent.click(screen.getByRole('button', { name: 'Open the policy draft' }))
    await waitFor(() => expect(screen.getByTestId('where').textContent).toBe(`/orgs/${ORG}/settings?tab=policy&prefill=orchestrator.host&value=claude-code`))
    expect(calls.some((c) => c.method !== 'GET')).toBe(false)
  })

  it('runs the doctor for the chosen host', async () => {
    const calls = stubApi((c) => (c.path === `${base}/catherd/doctor` ? { status: 202, body: { id: 'op_doc', kind: 'catherd_doctor', status: 'pending', created_at: NOW, updated_at: NOW, subject: { kind: 'organization', id: ORG }, revision: 1, result: null, problem: null } } : api(c)))
    tab()
    const option = ((await screen.findByLabelText('claude-code')) as HTMLInputElement).closest('[data-host]') as HTMLElement
    await userEvent.click(within(option).getByRole('button', { name: /Run doctor for this host/ }))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.path.endsWith('/catherd/doctor'))).toBe(true))
    expect(calls.find((c) => c.method === 'POST')!.body).toEqual({ host: 'claude-code' })
  })
})

describe('policy draft prefill', () => {
  it('opens the org policy draft with the host value filled in and unsaved', async () => {
    stubApi((c) => {
      if (c.path === `${base}/policy/schema`) return { body: [{ key: 'orchestrator.host', type: 'enum', enum: ['codex', 'claude-code'], default: 'codex', locked: false, project_overridable: false }] }
      if (c.path === `${base}/policy/draft`) return { body: { overrides: {}, revision: 1, differs_from_published: false } }
      if (c.path === `${base}/policy/effective`) return { body: { hash: 'h', items: [{ key: 'orchestrator.host', value: 'codex', default: 'codex', locked: false, source: { layer: 'product', number: 1 } }] } }
      if (c.path === `${base}/policy/versions`) return { body: { items: [], next_cursor: null } }
      return undefined
    })
    renderRoute(<OrgPolicyPanel orgId={ORG} />, '/x?prefill=orchestrator.host&value=claude-code', '/x')
    expect(await screen.findByText(/orchestrator.host is set to claude-code in this draft/)).toBeInTheDocument()
    expect(screen.getByText('Unsaved changes')).toBeInTheDocument()
    expect(screen.getByRole('combobox', { name: 'Orchestrator host' })).toHaveTextContent('Claude Code')
  })
})

describe('fa', () => {
  it('renders the keys tab in Persian with the environment names kept left-to-right', async () => {
    applyLocale('fa')
    stubApi(api)
    tab()
    const env = await screen.findByText('TYPESAFE_API_KEY')
    expect(env.closest('bdi')).toHaveAttribute('dir', 'ltr')
    expect(screen.getByText('کلید Jev')).toBeInTheDocument()
    expect(screen.getByText(/هرگز دوباره نمایش داده نمی‌شوند/)).toBeInTheDocument()
  })
})
