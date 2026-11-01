import { useQueryClient } from '@tanstack/react-query'
import {
  useApproveExternalOperations as useApproveMutation,
  useGetExternalOperation,
  useGetRunDiff,
  useListRunExternalOperations,
  useListRunFindings,
  useListRunRoles,
  useListRunVerification,
  useWaiveFinding as useWaiveMutation,
} from '../generated/runs/runs'
import { useGetMergeRequest, useListMergeRequests, useRefreshMergeRequest as useRefreshMutation } from '../generated/merge-requests/merge-requests'
import type { ListMergeRequestsParams } from '../generated/model'
import { ifMatch } from '../mutator'
import { invalidatePath } from '../query-keys'
import { ok } from '../problem'

export function useRunRoles(orgId: string, runId: string | undefined | null) {
  return useListRunRoles(orgId, runId ?? '', { query: { enabled: !!runId, select: (res) => ok(res) } })
}
export function useRunVerification(orgId: string, runId: string | undefined | null) {
  return useListRunVerification(orgId, runId ?? '', { query: { enabled: !!runId, select: (res) => ok(res).items } })
}
export function useRunDiff(orgId: string, runId: string | undefined | null, enabled = true) {
  return useGetRunDiff(orgId, runId ?? '', undefined, { query: { enabled: enabled && !!runId, retry: false, select: (res) => ok(res) } })
}
export function useRunFindings(orgId: string, runId: string | undefined | null) {
  return useListRunFindings(orgId, runId ?? '', undefined, { query: { enabled: !!runId, select: (res) => ok(res).items } })
}
export function useRunOperations(orgId: string, runId: string | undefined | null) {
  return useListRunExternalOperations(orgId, runId ?? '', { query: { enabled: !!runId, select: (res) => ok(res).items } })
}
export function useOperationDetail(orgId: string, xopId: string | undefined) {
  return useGetExternalOperation(orgId, xopId ?? '', { query: { enabled: !!xopId, select: (res) => ok(res) } })
}
export function useMergeRequests(orgId: string, params: ListMergeRequestsParams = {}) {
  return useListMergeRequests(orgId, { limit: 100, ...params }, { query: { select: (res) => ok(res).items } })
}
export function useMergeRequest(orgId: string, mrqId: string | undefined | null) {
  return useGetMergeRequest(orgId, mrqId ?? '', { query: { enabled: !!mrqId, select: (res) => ok(res) } })
}

function useInvalidateRun(orgId: string) {
  const qc = useQueryClient()
  return () => invalidatePath(qc, (p) => p.startsWith(`/api/v1/orgs/${orgId}/runs`) || p.startsWith(`/api/v1/orgs/${orgId}/tasks`) || p.startsWith(`/api/v1/orgs/${orgId}/merge-requests`) || p.startsWith(`/api/v1/orgs/${orgId}/external-operations`))
}
interface Headers {
  revision: number
  idempotencyKey: string
}
const hdr = (h: Headers) => ({ headers: { 'If-Match': ifMatch(h.revision), 'Idempotency-Key': h.idempotencyKey } })
/** Approves the exact operations the user saw: each carries the intent hash that was shown. */
export function useApproveOperations(orgId: string, h: Headers) {
  const invalidate = useInvalidateRun(orgId)
  return useApproveMutation({ request: hdr(h), mutation: { onSuccess: invalidate } })
}
export function useWaiveFindingMutation(orgId: string, h: Headers) {
  const invalidate = useInvalidateRun(orgId)
  return useWaiveMutation({ request: hdr(h), mutation: { onSuccess: invalidate } })
}
export function useRefreshMergeRequestRun(orgId: string, idempotencyKey: string) {
  const invalidate = useInvalidateRun(orgId)
  return useRefreshMutation({ request: { headers: { 'Idempotency-Key': idempotencyKey } }, mutation: { onSuccess: invalidate } })
}
