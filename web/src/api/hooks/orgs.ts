import { useQueryClient } from '@tanstack/react-query'
import {
  getGetOrgQueryKey,
  getListOrgsQueryKey,
  useCreateOrg as useCreateOrgMutation,
  useGetOrg,
  useListOrgs,
  usePatchOrg as usePatchOrgMutation,
} from '../generated/organizations/organizations'
import type { OrganizationDto } from '../generated/model'
import { ifMatch } from '../mutator'
import { ok } from '../problem'

export type Org = OrganizationDto

export function useOrgs() {
  return useListOrgs({ limit: 200 }, { query: { select: (res) => ok(res).items } })
}

export function useOrg(orgId: string | undefined) {
  return useGetOrg(orgId ?? '', { query: { enabled: !!orgId, select: (res) => ok(res) } })
}

/** `idempotencyKey` is created once per dialog open (see api/idempotency.ts). */
export function useCreateOrg(idempotencyKey: string) {
  const qc = useQueryClient()
  return useCreateOrgMutation({
    request: { headers: { 'Idempotency-Key': idempotencyKey } },
    mutation: { onSuccess: () => qc.invalidateQueries({ queryKey: getListOrgsQueryKey() }) },
  })
}

export function usePatchOrg(orgId: string, revision: number) {
  const qc = useQueryClient()
  return usePatchOrgMutation({
    request: { headers: { 'If-Match': ifMatch(revision) } },
    mutation: {
      onSuccess: () => {
        void qc.invalidateQueries({ queryKey: getListOrgsQueryKey() })
        void qc.invalidateQueries({ queryKey: getGetOrgQueryKey(orgId) })
      },
    },
  })
}
