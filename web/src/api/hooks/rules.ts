import { useQueryClient } from '@tanstack/react-query'
import { useArchivePinnedRule as useArchiveMutation, useCreatePinnedRule as useCreateMutation, useListPinnedRules, usePatchPinnedRule as usePatchMutation } from '../generated/rules/rules'
import type { ListPinnedRulesParams } from '../generated/model'
import { ifMatch } from '../mutator'
import { invalidatePath } from '../query-keys'
import { ok } from '../problem'

export function usePinnedRules(orgId: string, params: ListPinnedRulesParams = {}) {
  return useListPinnedRules(orgId, params, { query: { select: (res) => ok(res) } })
}
function useInvalidateRules(orgId: string) {
  const qc = useQueryClient()
  return () => invalidatePath(qc, (p) => p.startsWith(`/api/v1/orgs/${orgId}/rules`))
}
export function useCreateRule(orgId: string) {
  const invalidate = useInvalidateRules(orgId)
  return useCreateMutation({ mutation: { onSuccess: invalidate } })
}
export function usePatchRule(orgId: string, revision: number) {
  const invalidate = useInvalidateRules(orgId)
  return usePatchMutation({ request: { headers: { 'If-Match': ifMatch(revision) } }, mutation: { onSuccess: invalidate } })
}
export function useArchiveRule(orgId: string, revision: number) {
  const invalidate = useInvalidateRules(orgId)
  return useArchiveMutation({ request: { headers: { 'If-Match': ifMatch(revision) } }, mutation: { onSuccess: invalidate } })
}
