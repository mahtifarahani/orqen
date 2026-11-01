import { useQueryClient } from '@tanstack/react-query'
import { useGetExternalWriteSettings, usePutExternalWriteSettings as usePutMutation, usePutExternalWritesPause as usePauseMutation } from '../generated/external-writes/external-writes'
import { ifMatch } from '../mutator'
import { invalidatePath } from '../query-keys'
import { ok } from '../problem'

/** The phrase the user types to switch to live writes (checked by the server). */
export const LIVE_PHRASE = 'ENABLE LIVE WRITES'

export function useExternalWrites(orgId: string) {
  return useGetExternalWriteSettings(orgId, { query: { select: (res) => ok(res) } })
}
function useInvalidate(orgId: string) {
  const qc = useQueryClient()
  return () => {
    invalidatePath(qc, (p) => p.startsWith(`/api/v1/orgs/${orgId}/external-writes`))
    invalidatePath(qc, (p) => p.includes('/readiness'))
  }
}
export function usePutExternalWrites(orgId: string, revision: number) {
  const invalidate = useInvalidate(orgId)
  return usePutMutation({ request: { headers: { 'If-Match': ifMatch(revision) } }, mutation: { onSuccess: invalidate } })
}
export function usePauseExternalWrites(orgId: string, revision: number) {
  const invalidate = useInvalidate(orgId)
  return usePauseMutation({ request: { headers: { 'If-Match': ifMatch(revision) } }, mutation: { onSuccess: invalidate } })
}
