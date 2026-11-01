import { useQueryClient } from '@tanstack/react-query'
import {
  useCheckWriteCredential as useCheckMutation,
  useDeleteWriteCredential as useDeleteMutation,
  useGetWriteCredential,
  usePutWriteCredential as usePutMutation,
} from '../generated/connections/connections'
import { ifMatch } from '../mutator'
import { invalidatePath } from '../query-keys'
import { ok } from '../problem'

export function useWriteCredential(orgId: string, conId: string) {
  return useGetWriteCredential(orgId, conId, { query: { select: (res) => ok(res) } })
}
function useInvalidate(orgId: string, conId: string) {
  const qc = useQueryClient()
  return () => {
    invalidatePath(qc, (p) => p.startsWith(`/api/v1/orgs/${orgId}/connections/${conId}/write-credential`))
    invalidatePath(qc, (p) => p.includes('/readiness'))
  }
}
/** Write-only: the secret is never read back. */
export function usePutWriteCredentialSecret(orgId: string, conId: string, revision: number) {
  const invalidate = useInvalidate(orgId, conId)
  return usePutMutation({ request: { headers: { 'If-Match': ifMatch(revision) } }, mutation: { onSuccess: invalidate } })
}
export function useDeleteWriteCredentialSecret(orgId: string, conId: string, revision: number) {
  const invalidate = useInvalidate(orgId, conId)
  return useDeleteMutation({ request: { headers: { 'If-Match': ifMatch(revision) } }, mutation: { onSuccess: invalidate } })
}
/** A GET-only inspection, started as an operation. */
export function useCheckWriteCredentialRun(idempotencyKey: string) {
  return useCheckMutation({ request: { headers: { 'Idempotency-Key': idempotencyKey } } })
}
