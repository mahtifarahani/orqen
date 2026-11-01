import { useQueryClient } from '@tanstack/react-query'
import { useAttestHostSpike as useAttestMutation, useGetHost, useProbeHost as useProbeMutation } from '../generated/host/host'
import { ifMatch } from '../mutator'
import { invalidatePath } from '../query-keys'
import { ok } from '../problem'

export function useHost(orgId: string) {
  return useGetHost(orgId, { query: { select: (res) => ok(res) } })
}
/** Starts a probe operation; the caller owns the Idempotency-Key (see useOperationRunner). */
export function useProbeHost(idempotencyKey: string) {
  return useProbeMutation({ request: { headers: { 'Idempotency-Key': idempotencyKey } } })
}
export function useAttestSpike(orgId: string, revision: number) {
  const qc = useQueryClient()
  return useAttestMutation({
    request: { headers: { 'If-Match': ifMatch(revision) } },
    mutation: { onSuccess: () => invalidatePath(qc, (p) => p.startsWith(`/api/v1/orgs/${orgId}/host`)) },
  })
}
