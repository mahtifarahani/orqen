import { useQueryClient } from '@tanstack/react-query'
import { useGetAutomationState, usePutAutomationState as usePutMutation } from '../generated/automation/automation'
import { ifMatch } from '../mutator'
import { invalidatePath } from '../query-keys'
import { ok } from '../problem'

export function useAutomationState(orgId: string) {
  return useGetAutomationState(orgId, { query: { select: (res) => ok(res) } })
}
export function usePutAutomation(orgId: string, revision: number) {
  const qc = useQueryClient()
  return usePutMutation({
    request: { headers: { 'If-Match': ifMatch(revision) } },
    mutation: { onSuccess: () => invalidatePath(qc, (p) => p.startsWith(`/api/v1/orgs/${orgId}/automation`) || p.startsWith(`/api/v1/orgs/${orgId}/tasks`) || p.startsWith(`/api/v1/orgs/${orgId}/projects`)) },
  })
}
