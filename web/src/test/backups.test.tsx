import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { applyLocale } from '@/i18n/direction'
import { BackupsSettingsTab } from '@/features/orgs/backups-settings-tab'
import { OrgMaintenanceTab } from '@/features/orgs/maintenance-tab'
import { NOW, ORG, problem, renderRoute, stubApi, type Call } from './helpers'

const base = `/api/v1/orgs/${ORG}/system`
const settings = { enabled: true, interval_hours: 24, keep_last: 7, keep_days: 30, dir: '/b', default_dir: '/b', on_artifact_pattern: 'fail', revision: 4 }
const preview = {
  name: 'bk_1', integrity: [], integrity_ok: true, schema_version: 3, schema_compatible: true, target_empty: false, target_non_empty: ['runs'], counts: { runs: 2 },
  orgs: [{ id: 'org_X', slug: 'acme', name: 'Acme' }], missing_credentials: [{ ref: 'r', kind: 'connection', owner: 'con_1', org_id: 'org_X', present: false }], held_runs: 2, republish_profiles: [], can_restore: false, notes: [],
}
let calls: Call[]
let lastRestore: ReturnType<typeof problem> | { body: unknown }
const api = (c: Call) => {
  if (c.path === `${base}/backups/settings` && c.method === 'GET') return { body: settings }
  if (c.path === `${base}/backups/settings` && c.method === 'PUT') return { body: { ...settings, revision: 5 } }
  if (c.path === `${base}/backups`) return { body: { dir: '/b', entries: [{ name: 'bk_1', created_at: NOW, bytes: 1200, orgs: [], valid: true }], runs: [], next_due_at: null } }
  if (c.path === `${base}/backups/bk_1/verify`) return { body: preview }
  if (c.path === `${base}/backups/prune-preview`) return { body: { delete: ['bk_0'], keep: ['bk_1'] } }
  if (c.path === `${base}/backups/prune`) return { body: { deleted: ['bk_0'] } }
  if (c.path === `${base}/restore/last`) return lastRestore
  return undefined
}
beforeEach(() => {
  applyLocale('en')
  lastRestore = problem(404, 'not_found')
  calls = stubApi(api)
})
afterEach(() => {
  vi.unstubAllGlobals()
  applyLocale('en')
})

describe('backups settings', () => {
  it('saves the schedule with the settings revision as If-Match', async () => {
    renderRoute(<BackupsSettingsTab orgId={ORG} />, '/x', '/x')
    const keep = await screen.findByLabelText('Keep the last')
    await userEvent.clear(keep)
    await userEvent.type(keep, '10')
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'PUT')).toBe(true))
    const put = calls.find((c) => c.method === 'PUT')!
    expect(put.headers.get('If-Match')).toBe('"4"')
    expect(put.body).toMatchObject({ keep_last: 10, interval_hours: 24, on_artifact_pattern: 'fail' })
  })

  it('shows the restore preview on the end side with the CLI command and no restore button', async () => {
    renderRoute(<BackupsSettingsTab orgId={ORG} />, '/x', '/x')
    await userEvent.click(await screen.findByRole('button', { name: 'Verify and preview' }))
    const sheet = await screen.findByTestId('restore-preview')
    expect(sheet.className).toMatch(/\bend-0\b/)
    expect(await within(sheet).findByText(/1 credential must be entered again/)).toBeInTheDocument()
    expect(within(sheet).getByText(/2 runs would be held/)).toBeInTheDocument()
    expect(within(sheet).getByText(/orqen restore --from \/b\/bk_1 --dry-run/)).toBeInTheDocument()
    expect(within(sheet).queryByRole('button', { name: /^Restore/ })).not.toBeInTheDocument()
    expect(calls.find((c) => c.path.endsWith('/verify'))!.headers.get('Idempotency-Key')).toBeTruthy()
  })

  it('previews a prune and only deletes after the confirmation', async () => {
    renderRoute(<BackupsSettingsTab orgId={ORG} />, '/x', '/x')
    await userEvent.click(await screen.findByRole('button', { name: 'Clean up' }))
    const dialog = await screen.findByRole('dialog', { name: 'Remove old backups' })
    expect(calls.some((c) => c.path.endsWith('/prune'))).toBe(false)
    await userEvent.click(await within(dialog).findByRole('button', { name: 'Remove 1 backup' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.path.endsWith('/prune'))).toBe(true))
    expect(calls.find((c) => c.method === 'POST' && c.path.endsWith('/prune'))!.body).toEqual({ confirm: true })
  })
})

describe('maintenance', () => {
  it('says so when no restore happened', async () => {
    renderRoute(<OrgMaintenanceTab orgId={ORG} />, '/x', '/x')
    expect(await screen.findByText('This install has not been restored from a backup.')).toBeInTheDocument()
  })

  it('lists the credentials to enter again after a restore', async () => {
    lastRestore = { body: { id: 'r1', backup_name: 'bk_1', created_at: NOW, orgs: ['org_X'], counts: {}, blobs: 3, held_runs: 1, missing_credentials: [{ org_id: 'org_X', kind: 'connection', owner: 'con_1', ref: 'x' }], republish_profiles: [{ org_id: 'org_X', name: 'team' }], post_check: {} } }
    renderRoute(<OrgMaintenanceTab orgId={ORG} />, '/x', '/x')
    const report = await screen.findByTestId('restore-report')
    expect(within(report).getByText('con_1')).toBeInTheDocument()
    expect(within(report).getByText('1 run is held until you resume it.')).toBeInTheDocument()
    expect(within(report).getByText('team')).toBeInTheDocument()
  })
})
