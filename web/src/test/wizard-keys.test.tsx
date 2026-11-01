import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import i18n from '@/i18n'
import { ModelsPage } from '@/features/models/models-page'
import { SetupPage } from '@/features/setup/setup-page'
import { SystemCheckPanel } from '@/features/setup/system-check-panel'
import { applyLocale } from '@/i18n/direction'
import { readinessText } from '@/lib/readiness-text'
import { NOW, ORG, renderRoute, stubApi, type Call } from './helpers'

const base = `/api/v1/orgs/${ORG}`
const KEYS = ['organization', 'connections', 'projects', 'profile', 'policy', 'system', 'review']
const step = (key: string, status = 'todo', extra: Record<string, unknown> = {}) => ({ key, status, attention: false, missing: [], consequences: [], skip_reason: null, ...extra })
const wizard = (current: string) => ({
  current_step: current, dismissed: false, completed_at: null, pending_count: 7, revision: 3,
  steps: KEYS.map((k) => step(k, 'todo', k === 'profile' ? { consequences: ['automation_blocked', 'no_runs', 'jev_fallback_rules'], missing: [{ code: 'no_ready_profile_bound', detail: 'x' }] } : {})),
})
const secret = (kind: string, env: string, over: Record<string, unknown> = {}) => ({ kind, env_name: env, state: 'none', updated_at: null, revision: 0, last_check: null, verifiable: true, used_by: ['doctor'], required_reason: null, ...over })
const check = (id: string, status: string, extra: Record<string, unknown> = {}) => ({ id, group: 'catherd', status, detail: '', fix_code: '', fix_command: [], required: false, blocks: [], duration_ms: 1, ...extra })

const api = (c: Call) => {
  if (c.path === `${base}/setup` && c.method === 'GET') return { body: wizard('profile') }
  if (c.path === `${base}/catherd/secrets`) return { body: [secret('jev', 'TYPESAFE_API_KEY'), secret('xai', 'XAI_API_KEY', { required_reason: 'isolated_harness' })] }
  if (c.path === `${base}/catherd/hosts`) return { body: [{ host: 'codex', effective: true, catherd_supported: true, orqen_host_adapter: 'available', last_doctor: null }, { host: 'claude-code', effective: false, catherd_supported: true, orqen_host_adapter: 'unavailable', last_doctor: null }] }
  if (c.path === `${base}/catherd/status`) return { body: { available: true, supported: true, version: '1.5.0', home_display: '~', bin: '/x', host: 'codex' } }
  if (c.path === `${base}/policy/effective`) return { body: { hash: 'h', items: [{ key: 'orchestrator.host', value: 'codex', default: 'codex', locked: false, source: { layer: 'product', number: 1 } }] } }
  if (c.path === `${base}/model-profiles`) return { body: { items: [], next_cursor: null } }
  if (c.path === `${base}/catherd/personal-profiles`) return { body: { available: false, profiles: [] } }
  return undefined
}

beforeEach(() => applyLocale('en'))
afterEach(() => {
  vi.unstubAllGlobals()
  applyLocale('en')
})

describe('wizard profile step', () => {
  it('embeds the host and keys cards above the profile quick start', async () => {
    stubApi(api)
    renderRoute(<SetupPage />, `/orgs/${ORG}/setup?step=profile`, '/orgs/:orgId/setup')
    const region = await screen.findByRole('region', { name: 'Orchestrator host and keys' })
    expect(await within(region).findByText('TYPESAFE_API_KEY')).toBeInTheDocument()
    expect(within(region).getByLabelText('codex')).toBeChecked()
    expect(within(region).getByText('catherd keys')).toBeInTheDocument()
    // The profile quick start (the profile list) comes after the cards.
    const quick = await screen.findByRole('button', { name: /New profile/ })
    expect(region.compareDocumentPosition(quick) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('the skip dialog says what happens without a Jev key', async () => {
    const calls = stubApi(api)
    renderRoute(<SetupPage />, `/orgs/${ORG}/setup?step=profile`, '/orgs/:orgId/setup')
    await userEvent.click(await screen.findByRole('button', { name: 'Skip this step' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText("Without a Jev key, routing uses the lanes' kind and difficulty.")).toBeInTheDocument()
    expect(calls.some((c) => c.path.endsWith('/skip'))).toBe(false)
  })

  it('in Persian the cards flow right-to-left and keep identifiers left-to-right', async () => {
    applyLocale('fa')
    stubApi(api)
    renderRoute(<SetupPage />, `/orgs/${ORG}/setup?step=profile`, '/orgs/:orgId/setup')
    const region = await screen.findByRole('region', { name: 'میزبان هماهنگ‌کننده و کلیدها' })
    const env = await within(region).findByText('TYPESAFE_API_KEY')
    expect(document.documentElement.dir).toBe('rtl')
    expect(env.closest('bdi')).toHaveAttribute('dir', 'ltr')
    // No Radix root in the cards forces ltr.
    expect(region.closest('[dir="ltr"]')).toBeNull()
    expect(within(region).getByText('کلید Jev')).toBeInTheDocument()
  })
})

describe('Models page tab', () => {
  it('opens the Orchestrator & keys tab from the URL and lays out right-to-left in Persian', async () => {
    applyLocale('fa')
    stubApi(api)
    renderRoute(<ModelsPage />, `/orgs/${ORG}/models?tab=orchestrator`, '/orgs/:orgId/models')
    const tab = await screen.findByRole('tab', { name: 'میزبان و کلیدها', selected: true })
    expect(tab.closest('[data-slot="tabs"]')).toHaveAttribute('dir', 'rtl')
    expect(await screen.findByText('TYPESAFE_API_KEY')).toBeInTheDocument()
  })
})

describe('system check rows for the new checks', () => {
  const report = {
    checked_at: NOW,
    checks: [
      check('catherd.jev_key', 'warn', { fix_code: 'jev_key_missing', blocks: ['jev_routing'], detail: 'no key' }),
      check('catherd.backend_keys', 'warn', { fix_code: 'backend_key_missing' }),
      check('catherd.credentials_file', 'warn', { fix_code: 'unexpected_credentials_file' }),
      check('bin.opencode_login', 'warn', { group: 'binaries', fix_code: 'opencode_logged_out', fix_command: ['opencode', 'auth', 'login'] }),
    ],
  }
  it('names each check, explains the fix and links the key checks to the keys tab', async () => {
    stubApi((c) => (c.path === `${base}/system/preflight` ? { body: report } : c.path === `${base}/system/launchd` ? { body: { opted_in: false, plist_path: 'p', plist_xml: '', install_commands: [], uninstall_commands: [], revision: 0 } } : undefined))
    renderRoute(<SystemCheckPanel orgId={ORG} />, '/x', '/x')
    const jev = (await screen.findByText('catherd.jev_key')).closest('li') as HTMLElement
    expect(within(jev).getByText('Jev key')).toBeInTheDocument()
    expect(within(jev).getByText(/Enter a Jev key under Models/)).toBeInTheDocument()
    expect(within(jev).getByText(/Jev routing/)).toBeInTheDocument()
    expect(within(jev).getByRole('link', { name: 'Open Orchestrator & keys' })).toHaveAttribute('href', `/orgs/${ORG}/models?tab=orchestrator`)
    expect(within((screen.getByText('catherd.backend_keys')).closest('li') as HTMLElement).getByRole('link', { name: 'Open Orchestrator & keys' })).toBeInTheDocument()
    const file = (screen.getByText('catherd.credentials_file')).closest('li') as HTMLElement
    expect(within(file).getByText('catherd credentials file')).toBeInTheDocument()
    expect(within(file).getByText(/Orqen never writes this file/)).toBeInTheDocument()
    expect(within(file).queryByRole('link')).toBeNull()
    const oc = (screen.getByText('bin.opencode_login')).closest('li') as HTMLElement
    expect(within(oc).getByText('opencode login')).toBeInTheDocument()
    expect(within(oc).getByText('opencode auth login').closest('bdi')).toHaveAttribute('dir', 'ltr')
    expect(screen.getByRole('heading', { name: 'catherd keys', level: 3 })).toBeInTheDocument()
  })
})

describe('readiness texts', () => {
  it('translates codes that carry an argument and keeps them out of i18next namespaces', async () => {
    const t = i18n.getFixedT('en')
    expect(readinessText(t, 'blockers', 'backend_key_missing:grok')).toBe('The isolated grok harness needs its key, and none is set.')
    expect(readinessText(t, 'blockers', 'backend_key_refused:cursor')).toBe('The key for the isolated cursor harness was refused.')
    expect(readinessText(t, 'warnings', 'jev_key_missing')).toMatch(/^No Jev key is set/)
    expect(readinessText(t, 'blockers', 'profile_host_mismatch')).toMatch(/another orchestrator host/)
    expect(readinessText(t, 'blockers', 'host_adapter_unavailable')).toMatch(/no execution adapter/)
    expect(readinessText(t, 'blockers', 'unknown_code:x', 'server detail')).toBe('server detail')
    const fa = i18n.getFixedT('fa')
    expect(readinessText(fa, 'blockers', 'backend_key_missing:grok')).toContain('grok')
    await waitFor(() => expect(true).toBe(true))
  })
})
