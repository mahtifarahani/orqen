import { useQueryClient } from '@tanstack/react-query'
import {
  useDeleteCodeIndex,
  useGetGraphStatus,
  useListCodeIndexes,
  usePreviewCodeIndexRetention,
  useQueryCodeImpact,
  useStartCodeIndex,
} from '../generated/graph/graph'
import { ifMatch } from '../mutator'
import { invalidatePath } from '../query-keys'
import { ok } from '../problem'

export function useCodeGraphStatus(orgId: string) {
  return useGetGraphStatus(orgId, { query: { select: (res) => ok(res).code_graph } })
}
export function useCodeIndexes(orgId: string, projectId?: string) {
  return useListCodeIndexes(orgId, projectId ? { project_id: projectId } : {}, { query: { select: (res) => ok(res).items } })
}
export function useRetentionPreview(orgId: string, enabled: boolean) {
  return usePreviewCodeIndexRetention(orgId, { query: { enabled, select: (res) => ok(res) } })
}
export function useCodeImpact(orgId: string, cixId: string | null, target: string, kind: string, depth: number) {
  return useQueryCodeImpact(orgId, cixId ?? '', { target, kind, depth, limit: 20 }, { query: { enabled: !!cixId && target.trim() !== '', retry: false, select: (res) => ok(res) } })
}

function useInvalidate(orgId: string) {
  const qc = useQueryClient()
  return () => invalidatePath(qc, (p) => p.startsWith(`/api/v1/orgs/${orgId}/code-indexes`) || p.startsWith(`/api/v1/orgs/${orgId}/graph/status`))
}
/** Indexing is an operation: the caller owns the Idempotency-Key. */
export function useStartIndex(orgId: string, idempotencyKey: string) {
  return useStartCodeIndex({ request: { headers: { 'Idempotency-Key': idempotencyKey } }, mutation: { onSuccess: useInvalidate(orgId) } })
}
export function useRemoveIndex(orgId: string, revision: number) {
  return useDeleteCodeIndex({ request: { headers: { 'If-Match': ifMatch(revision) } }, mutation: { onSuccess: useInvalidate(orgId) } })
}
