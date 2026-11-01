import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { RunDto } from '@/api/generated/model'
import { MergeRequestTab } from '@/features/tasks/merge-request-tab'
import { PublishingTab } from '@/features/tasks/publishing-tab'
import { ReviewTab } from '@/features/tasks/review-tab'
import { applyLocale } from '@/i18n/direction'
import { NOW, ORG, problem, renderRoute, stubApi, type Call } from './helpers'

const base = `/api/v1/orgs/${ORG}`
const run = {
  id: 'run_1',
  revision: 7,
  publish_mode: 'live',
  merge_request_id: 'mrq_1',
  heads: { base: 'a', head: 'h1', verified: 'h1', reviewed: 'h1', pushed: '', remote: '' },
  review: { round: 1, verdict: 'request_changes', head: 'h1', blocking_open: 1, independence: { status: 'independent', reason: '', coder_families: [], reviewer_family: '', shared: [] } },
} as unknown as RunDto
const op = {
  id: 'xop_1', run_id: 'run_1', action: 'git_push', intent_key: 'k', intent_hash: 'hash_abc', head: 'h1', round: 1,
  target: { host: '127.0.0.1:1234', project_path: 'g/app', branch: 'orqen/orq-1', mr_iid: 0 }, decision: { outcome: 'awaiting_approval', reasons: ['needs_approval'], decided_at: NOW, settings_revision: 1, policy_version_id: null, env_scope: 'loopback_only' },
  approval: null, state: 'awaiting_approval', attempts: 0, remote: null, error: null, created_at: NOW, updated_at: NOW,
}
let approveStatus = 200
const api = (c: Call) => {
  if (c.path === `${base}/runs/run_1/external-operations` && c.method === 'GET') return { body: { items: [op], next_cursor: null } }
  if (c.path === `${base}/external-operations/xop_1`) return { body: { ...op, payload: {}, preview: { title: 'Push', body: '', diffstat: ' a.txt | 1 +', commits: [{ sha: 'abcdef1234567', subject: 'Add a' }], refspec: 'h1:refs/heads/orqen/orq-1', url: '' } } }
  if (c.path === `${base}/runs/run_1/external-operations/approve`) return approveStatus === 200 ? { body: run } : problem(409, 'intent_changed')
  if (c.path === `${base}/runs/run_1/findings`) return { body: { items: [{ id: 'fnd_1', run_id: 'run_1', round: 1, head_sha: 'h1', finding_key: 'k', severity: 'blocker', blocking: true, file: 'src/a.go', line: 4, side: 'new', location_valid: true, inline_eligible: false, title: 'Nil deref', body: 'It crashes.', acceptance_ref: '', status: 'open', waiver: null, publication: null, created_at: NOW, revision: 3 }], next_cursor: null } }
  if (c.path === `${base}/runs/run_1/findings/fnd_1/waive`) return { body: {} }
  if (c.path === `${base}/merge-requests/mrq_1`) return { body: { id: 'mrq_1', run_id: 'run_1', project_id: 'p', mode: 'dry_run', iid: null, web_url: null, title: 'ORQ-1', source_branch: 'orqen/orq-1', target_branch: 'main', draft: true, state: 'opened', current_head: 'h1', last_pushed_sha: '', verified_head: 'h1', reviewed_head: 'h1', pipeline: null, discussions_resolved: true, has_conflicts: false, readiness: { ready: false, evaluated_at: NOW, head: 'h1', checks: [{ code: 'remote_mr', status: 'fail', detail: 'simulated' }] }, merged: null, created_at: NOW, updated_at: NOW, revision: 1 } }
  return undefined
}
beforeEach(() => {
  applyLocale('en')
  approveStatus = 200
})
afterEach(() => vi.unstubAllGlobals())
const path = `/orgs/${ORG}/tasks/t`

describe('task detail, Sprint 4', () => {
  it('shows the exact payload and approves with the intent hash that was shown', async () => {
    const calls = stubApi(api)
    renderRoute(<PublishingTab orgId={ORG} run={run} />, path, '/orgs/:orgId/tasks/:taskId')
    await userEvent.click(await screen.findByRole('button', { name: /Review 1 operation/ }))
    const dialog = await screen.findByRole('dialog')
    expect(await within(dialog).findByText('h1:refs/heads/orqen/orq-1')).toBeInTheDocument()
    expect(within(dialog).getByText(/a\.txt \| 1 \+/)).toBeInTheDocument()
    await userEvent.click(within(dialog).getByRole('button', { name: 'Approve and send' }))
    await waitFor(() => expect(calls.some((c) => c.path.endsWith('/approve'))).toBe(true))
    const post = calls.find((c) => c.path.endsWith('/approve'))!
    expect(post.body).toEqual({ operations: [{ id: 'xop_1', intent_hash: 'hash_abc' }] })
    expect(post.headers.get('If-Match')).toBe('"7"')
    expect(post.headers.get('Idempotency-Key')).toBeTruthy()
  })
  it('a changed operation (409) asks to review again and keeps the dialog open', async () => {
    approveStatus = 409
    stubApi(api)
    renderRoute(<PublishingTab orgId={ORG} run={run} />, path, '/orgs/:orgId/tasks/:taskId')
    await userEvent.click(await screen.findByRole('button', { name: /Review 1 operation/ }))
    const dialog = await screen.findByRole('dialog')
    await userEvent.click(await within(dialog).findByRole('button', { name: 'Approve and send' }))
    expect(await within(dialog).findByText(/changed after you opened it/)).toBeInTheDocument()
  })
  it('waives a blocking finding with the finding revision', async () => {
    const calls = stubApi(api)
    renderRoute(<ReviewTab orgId={ORG} run={run} />, path, '/orgs/:orgId/tasks/:taskId')
    expect(await screen.findByText('Nil deref')).toBeInTheDocument()
    expect(screen.getByText('In the summary note only')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Accept this finding' }))
    await userEvent.type(await screen.findByLabelText('Why is this acceptable?'), 'known')
    await userEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Accept this finding' }))
    await waitFor(() => expect(calls.some((c) => c.path.endsWith('/waive'))).toBe(true))
    const post = calls.find((c) => c.path.endsWith('/waive'))!
    expect(post.body).toEqual({ reason: 'known' })
    expect(post.headers.get('If-Match')).toBe('"3"')
  })
  it('the merge request tab marks a simulated MR and lists the readiness checks', async () => {
    stubApi(api)
    renderRoute(<MergeRequestTab orgId={ORG} run={run} />, path, '/orgs/:orgId/tasks/:taskId')
    expect(await screen.findByText('This merge request is simulated. Nothing was written to GitLab.')).toBeInTheDocument()
    expect(screen.getAllByText('DRY-RUN').length).toBeGreaterThan(0)
    expect(screen.getByText('The merge request exists on GitLab')).toBeInTheDocument()
  })
})
