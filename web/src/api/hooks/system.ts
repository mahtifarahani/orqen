import { useQueryClient } from '@tanstack/react-query'
import {
  useGetLaunchdPlan,
  useGetPreflight,
  usePatchLaunchd as usePatchMutation,
  useProbeKeychain as useProbeMutation,
  useStartPreflight as useStartMutation,
} from '../generated/system/system'
import { ifMatch } from '../mutator'
import { invalidatePath } from '../query-keys'
import { ok } from '../problem'

export function usePreflight(orgId: string) {
  return useGetPreflight(orgId, { query: { select: (res) => ok(res) } })
}
export function useLaunchdPlan(orgId: string) {
  return useGetLaunchdPlan(orgId, { query: { select: (res) => ok(res) } })
}
/** A new operation per Re-check; the caller owns the Idempotency-Key (see useOperationRunner). */
export function useStartPreflightRun(idempotencyKey: string) {
  return useStartMutation({ request: { headers: { 'Idempotency-Key': idempotencyKey } } })
}
export function useKeychainProbe(idempotencyKey: string) {
  return useProbeMutation({ request: { headers: { 'Idempotency-Key': idempotencyKey } } })
}
/** Records only the opt-in; nothing is written to the system. */
export function useLaunchdOptIn(orgId: string, revision: number) {
  const qc = useQueryClient()
  return usePatchMutation({
    request: { headers: { 'If-Match': ifMatch(revision) } },
    mutation: { onSuccess: () => invalidatePath(qc, (p) => p.startsWith(`/api/v1/orgs/${orgId}/system`)) },
  })
}
