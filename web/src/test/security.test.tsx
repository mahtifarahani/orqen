import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { applyLocale } from '@/i18n/direction'
import { SecurityPage } from '@/features/security/security-page'
import { SecurityTab } from '@/features/tasks/security-tab'
import { CodeGraphPanel } from '@/features/graph/code-panel'
import { NOW, ORG, renderRoute, stubApi, type Call } from './helpers'

const base = `/api/v1/orgs/${ORG}`
const scan = (over: Record<string, unknown> = {}) => ({
  id: 'scn_1', project_id: 'prj_1', run_id: 'run_1', head_sha: 'a'.repeat(40), target_kind: 'local_clone', tool_version: '1', config: { model: 'm1', budget_mode: 'capped', budget_usd: 5, max_turns: 50 },
  state: 'finished', completion: 'complete', tree_modified: false, tree_diff_artifact_id: null, report_artifact_id: null, stdout_artifact_id: null, counts: { by_severity: {}, blocking: 1 },
  started_at: NOW, finished_at: NOW, created_at: NOW, revision: 2, ...over,
})
const finding = (over: Record<string, unknown> = {}) => ({
  id: 'sfd_1', source: 'security', scan_id: 'scn_1', run_id: 'run_1', head_sha: 'a'.repeat(40), finding_key: 'k', severity: 'blocker', severity_raw: 'critical', blocking: true,
  file: 'src/app.go', line: 12, location_valid: true, cwe: 'CWE-79', title: 'Reflected input', body: 'Escape it.', proof_artifact_id: null, status: 'open', waiver: null, created_at: NOW, revision: 3, ...over,
})
let status: Record<string, unknown>
let scans: unknown[]
let findings: unknown[]
let calls: Call[]
const run = { id: 'run_1', heads: { reviewed: 'a'.repeat(40) } } as never

const api = (c: Call) => {
  if (c.path === `${base}/security/status`) return { body: status }
  if (c.path === `${base}/security/scans`) return { body: { items: scans, next_cursor: null } }
  if (c.path === `${base}/security/scans/scn_1/findings`) return { body: { items: findings } }
  if (c.path === `${base}/runs/run_1/security-findings`) return { body: { items: findings } }
  if (c.path.endsWith('/waive')) return { body: finding({ status: 'waived' }) }
  if (c.path === `${base}/runs/run_1/security-scans`) return { status: 202, body: { id: 'op_1', status: 'running' } }
  if (c.path === `${base}/graph/status`) return { body: { code_graph: { available: true, reason: null, ready_indexes: 1, newest: null } } }
  if (c.path === `${base}/code-indexes`) return { body: { items: [{ id: 'cix_1', project_id: 'prj_1', repo_key: 'api', commit_sha: 'b'.repeat(40), state: 'ready', tool_version: '1', bytes: 10, files: 2, error_code: null, started_at: NOW, finished_at: NOW, revision: 1 }] } }
  if (c.path === `${base}/code-indexes/cix_1/impact`) return { body: { commit_sha: 'b'.repeat(40), items: [{ path: 'src/b.go', symbol: null, relation: 'imports', depth: 1 }], truncated: false } }
  if (c.path === `${base}/projects`) return { body: { items: [], next_cursor: null } }
  return undefined
}

beforeEach(() => {
  applyLocale('en')
  status = { usable: { ok: true, reason: null }, policy: { scan: 'manual', gate: 'required', blocking_severity: 'high', model_set: true, budget_mode: 'capped' }, counts: { scans_running: 0, open_blocking: 1 } }
  scans = [scan()]
  findings = [finding()]
  calls = stubApi(api)
})
afterEach(() => {
  vi.unstubAllGlobals()
  applyLocale('en')
})

describe('security page', () => {
  it('shows the policy, the counts and a scan with its findings', async () => {
    renderRoute(<SecurityPage />, `/orgs/${ORG}/security`, '/orgs/:orgId/security')
    expect(await screen.findByText('Ready to scan')).toBeInTheDocument()
    expect(screen.getByText('Merge gate').nextSibling).toHaveTextContent('Required')
    await userEvent.click(await screen.findByRole('button', { name: 'Open' }))
    const sheet = await screen.findByTestId('scan-sheet')
    expect(await within(sheet).findByText('Reflected input')).toBeInTheDocument()
  })

  it('waives a blocking finding with a written reason and the finding revision', async () => {
    renderRoute(<SecurityPage />, `/orgs/${ORG}/security`, '/orgs/:orgId/security')
    await userEvent.click(await screen.findByRole('button', { name: 'Open' }))
    const sheet = await screen.findByTestId('scan-sheet')
    await userEvent.click(await within(sheet).findByRole('button', { name: 'Waive' }))
    const dialog = await screen.findByRole('dialog', { name: 'Waive this finding' })
    const submit = within(dialog).getByRole('button', { name: 'Waive finding' })
    expect(submit).toBeDisabled()
    await userEvent.type(within(dialog).getByLabelText('Why is this acceptable?'), 'internal only')
    await userEvent.click(submit)
    await waitFor(() => expect(calls.some((c) => c.path === `${base}/security-findings/sfd_1/waive`)).toBe(true))
    const c = calls.find((x) => x.path.endsWith('/waive'))!
    expect(c.headers.get('If-Match')).toBe('"3"')
    expect(c.body).toEqual({ text: 'internal only' })
  })
})

describe('task security tab', () => {
  it('starts a local clone scan with an idempotency key', async () => {
    renderRoute(<SecurityTab orgId={ORG} run={run} />, '/x', '/x')
    const btn = await screen.findByRole('button', { name: 'Scan reviewed code' })
    await waitFor(() => expect(btn).toBeEnabled())
    await userEvent.click(btn)
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.path.endsWith('/security-scans'))).toBe(true))
    const c = calls.find((x) => x.method === 'POST')!
    expect(c.body).toEqual({ target_kind: 'local_clone' })
    expect(c.headers.get('Idempotency-Key')).toBeTruthy()
  })

  it('disables scanning with the reason when the scanner is not usable', async () => {
    status = { ...status, usable: { ok: false, reason: 'not_registered' } }
    renderRoute(<SecurityTab orgId={ORG} run={run} />, '/x', '/x')
    expect(await screen.findByTestId('scan-blocked')).toHaveTextContent('No binary registered')
    expect(screen.getByRole('button', { name: 'Scan reviewed code' })).toBeDisabled()
  })
})

describe('code graph panel', () => {
  it('asks an impact question against a ready index', async () => {
    renderRoute(<CodeGraphPanel orgId={ORG} />, '/x', '/x')
    expect(await screen.findByText('1 ready index')).toBeInTheDocument()
    await userEvent.type(await screen.findByLabelText('Path or symbol'), 'src/a.go')
    await userEvent.click(screen.getByRole('button', { name: 'Ask' }))
    expect(await screen.findByText('src/b.go')).toBeInTheDocument()
    const c = calls.find((x) => x.path.endsWith('/impact'))!
    expect(c.search.get('target')).toBe('src/a.go')
    expect(c.search.get('kind')).toBe('file')
  })
})
