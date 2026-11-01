import { useQueryClient } from '@tanstack/react-query'
import {
  useCancelSecurityScan,
  useGetSecurityIssueDraft,
  useGetSecurityScan,
  useGetSecurityStatus,
  useListRunSecurityFindings,
  useListScanFindings,
  useListSecurityScans,
  useStartSecurityScan,
  useWaiveSecurityFinding,
} from '../generated/security/security'
import type { ListSecurityScansParams } from '../generated/model'
import { ifMatch } from '../mutator'
import { invalidatePath } from '../query-keys'
import { ok } from '../problem'

/** The status counts and policy are open objects in the contract; this is the shape the server fills. */
export interface SecurityStatus {
  usable: { ok: boolean; reason: string | null }
  policy: { scan?: string; gate?: string; blocking_severity?: string; model_set?: boolean; budget_mode?: string }
  counts: { scans_running?: number; open_blocking?: number }
}

export function useSecurityStatus(orgId: string) {
  return useGetSecurityStatus(orgId, { query: { select: (res) => ok(res) as unknown as SecurityStatus } })
}
export function useSecurityScans(orgId: string, params: ListSecurityScansParams = {}, enabled = true) {
  return useListSecurityScans(orgId, params, { query: { enabled, select: (res) => ok(res).items } })
}
export function useSecurityScan(orgId: string, scnId: string | null) {
  return useGetSecurityScan(orgId, scnId ?? '', { query: { enabled: !!scnId, select: (res) => ok(res) } })
}
export function useScanFindings(orgId: string, scnId: string | null) {
  return useListScanFindings(orgId, scnId ?? '', { query: { enabled: !!scnId, select: (res) => ok(res).items } })
}
export function useRunSecurityFindings(orgId: string, runId: string | null | undefined) {
  return useListRunSecurityFindings(orgId, runId ?? '', { query: { enabled: !!runId, select: (res) => ok(res).items } })
}
export function useIssueDraft(orgId: string, sfdId: string | null) {
  return useGetSecurityIssueDraft(orgId, sfdId ?? '', { query: { enabled: !!sfdId, select: (res) => ok(res) } })
}

function useInvalidate(orgId: string) {
  const qc = useQueryClient()
  return () => invalidatePath(qc, (p) => p.startsWith(`/api/v1/orgs/${orgId}/security`) || p.startsWith(`/api/v1/orgs/${orgId}/runs`) || p.startsWith(`/api/v1/orgs/${orgId}/merge-requests`))
}
/** A scan is an operation: the caller owns the Idempotency-Key (see useOperationRunner). */
export function useStartScan(orgId: string, idempotencyKey: string) {
  return useStartSecurityScan({ request: { headers: { 'Idempotency-Key': idempotencyKey } }, mutation: { onSuccess: useInvalidate(orgId) } })
}
export function useCancelScan(orgId: string, revision: number) {
  return useCancelSecurityScan({ request: { headers: { 'If-Match': ifMatch(revision) } }, mutation: { onSuccess: useInvalidate(orgId) } })
}
export function useWaiveSecurity(orgId: string, revision: number) {
  return useWaiveSecurityFinding({ request: { headers: { 'If-Match': ifMatch(revision) } }, mutation: { onSuccess: useInvalidate(orgId) } })
}
