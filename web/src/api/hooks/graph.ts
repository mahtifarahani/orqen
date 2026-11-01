import { useQueryClient } from '@tanstack/react-query'
import {
  useConfirmGraphEdge as useConfirmMutation,
  useGetGraphNeighborhood,
  useListGraphNodes,
  useRebuildGraph as useRebuildMutation,
  useRejectGraphEdge as useRejectMutation,
} from '../generated/graph/graph'
import { ifMatch } from '../mutator'
import { invalidatePath } from '../query-keys'
import { ok } from '../problem'

export function useGraphNodes(orgId: string, q: string) {
  return useListGraphNodes(orgId, q.trim() ? { q } : {}, { query: { select: (res) => ok(res) } })
}
export function useNeighborhood(orgId: string, node: string | undefined, depth: number) {
  return useGetGraphNeighborhood(orgId, { node, depth, limit: 200 }, { query: { enabled: !!node, select: (res) => ok(res) } })
}
function useInvalidate(orgId: string) {
  const qc = useQueryClient()
  return () => invalidatePath(qc, (p) => p.startsWith(`/api/v1/orgs/${orgId}/graph`))
}
export function useConfirmEdge(orgId: string, revision: number) {
  return useConfirmMutation({ request: { headers: { 'If-Match': ifMatch(revision) } }, mutation: { onSuccess: useInvalidate(orgId) } })
}
export function useRejectEdge(orgId: string, revision: number) {
  return useRejectMutation({ request: { headers: { 'If-Match': ifMatch(revision) } }, mutation: { onSuccess: useInvalidate(orgId) } })
}
export function useRebuild(orgId: string) {
  const invalidate = useInvalidate(orgId)
  // The rebuild is a background job: refresh now and again shortly after it has had time to finish.
  const refresh = () => {
    invalidate()
    for (const ms of [2000, 5000, 10000]) setTimeout(invalidate, ms)
  }
  return useRebuildMutation({ mutation: { onSuccess: refresh } })
}
