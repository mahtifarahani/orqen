import { useQueryClient } from '@tanstack/react-query'
import {
  useCheckCatherdSecret as useCheckSecretMutation,
  useDeleteCatherdSecret as useDeleteSecretMutation,
  useGetCatherdCatalog,
  useGetCatherdSchema,
  useGetCatherdStatus,
  useGetPersonalProfiles,
  useListCatherdSecrets,
  useListOrchestratorHosts,
  usePutCatherdSecret as usePutSecretMutation,
  useStartCatherdCatalog as useCatalogMutation,
  useStartDoctor as useDoctorMutation,
} from '../generated/catherd/catherd'
import type { GetCatherdCatalogParams } from '../generated/model'
import { ifMatch } from '../mutator'
import { invalidatePath } from '../query-keys'
import { ok } from '../problem'

export function useCatherdStatus(orgId: string) {
  return useGetCatherdStatus(orgId, { query: { select: (res) => ok(res) } })
}
export function useCatherdSchema(orgId: string) {
  return useGetCatherdSchema(orgId, { query: { select: (res) => ok(res), staleTime: 60_000 } })
}
export function useCatalog(orgId: string, params: GetCatherdCatalogParams = {}) {
  return useGetCatherdCatalog(orgId, params, { query: { select: (res) => ok(res) } })
}
export function usePersonalProfileNames(orgId: string, enabled = true) {
  return useGetPersonalProfiles(orgId, { query: { enabled, select: (res) => ok(res) } })
}
export function useStartDoctorRun(idempotencyKey: string) {
  return useDoctorMutation({ request: { headers: { 'Idempotency-Key': idempotencyKey } } })
}
export function useStartCatalogRefresh(idempotencyKey: string) {
  return useCatalogMutation({ request: { headers: { 'Idempotency-Key': idempotencyKey } } })
}

export function useOrchestratorHosts(orgId: string) {
  return useListOrchestratorHosts(orgId, { query: { select: (res) => ok(res) } })
}
export function useCatherdSecrets(orgId: string) {
  return useListCatherdSecrets(orgId, { query: { select: (res) => ok(res) } })
}
/** Keys change what catherd gets and what readiness says, so every catherd and project read refreshes. */
export function useInvalidateKeys(orgId: string) {
  const qc = useQueryClient()
  return () => invalidatePath(qc, (p) => p.startsWith(`/api/v1/orgs/${orgId}/catherd`) || p.startsWith(`/api/v1/orgs/${orgId}/projects`) || p.startsWith(`/api/v1/orgs/${orgId}/system`) || p.startsWith(`/api/v1/orgs/${orgId}/setup`))
}
/** Write-only: the secret travels only in this PUT body and is never returned. */
export function usePutCatherdSecretValue(orgId: string, revision: number) {
  const invalidate = useInvalidateKeys(orgId)
  return usePutSecretMutation({ request: { headers: { 'If-Match': ifMatch(revision) } }, mutation: { onSettled: invalidate } })
}
export function useRemoveCatherdSecret(orgId: string, revision: number) {
  const invalidate = useInvalidateKeys(orgId)
  return useDeleteSecretMutation({ request: { headers: { 'If-Match': ifMatch(revision) } }, mutation: { onSettled: invalidate } })
}
/** A key check is an operation; the caller owns the Idempotency-Key (see useOperationRunner). */
export function useCheckCatherdSecretRun(idempotencyKey: string) {
  return useCheckSecretMutation({ request: { headers: { 'Idempotency-Key': idempotencyKey } } })
}
