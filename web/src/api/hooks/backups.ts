import { useQueryClient } from '@tanstack/react-query'
import {
  useGetBackupSettings,
  useGetLastRestore,
  useGetSupportBundle,
  useListBackups,
  usePreviewBackupPrune,
  usePruneBackups,
  usePutBackupSettings,
  useStartBackup,
  useVerifyBackup,
} from '../generated/system/system'
import { ifMatch } from '../mutator'
import { invalidatePath } from '../query-keys'
import { ok } from '../problem'

export function useBackupSettings(orgId: string) {
  return useGetBackupSettings(orgId, { query: { select: (res) => ok(res) } })
}
export function useBackups(orgId: string) {
  return useListBackups(orgId, { query: { select: (res) => ok(res) } })
}
export function usePrunePreview(orgId: string, enabled: boolean) {
  return usePreviewBackupPrune(orgId, { query: { enabled, select: (res) => ok(res) } })
}
export function useLastRestore(orgId: string) {
  return useGetLastRestore(orgId, { query: { retry: false, select: (res) => ok(res) } })
}
/** Fetched only on demand (the person pressed Download). */
export function useSupportBundle(orgId: string) {
  return useGetSupportBundle(orgId, { query: { enabled: false, select: (res) => ok(res) } })
}

/** Backup events carry no organization, so the org event stream never announces them: refresh the lists when the tracked operation ends. */
export function useRefreshBackups(orgId: string) {
  return useInvalidate(orgId)
}
function useInvalidate(orgId: string) {
  const qc = useQueryClient()
  return () => invalidatePath(qc, (p) => p.startsWith(`/api/v1/orgs/${orgId}/system/backups`))
}
export function usePutSettings(orgId: string, revision: number) {
  return usePutBackupSettings({ request: { headers: { 'If-Match': ifMatch(revision) } }, mutation: { onSuccess: useInvalidate(orgId) } })
}
/** A backup is an operation: the caller owns the Idempotency-Key (see useOperationRunner). */
export function useStartBackupRun(orgId: string, idempotencyKey: string) {
  return useStartBackup({ request: { headers: { 'Idempotency-Key': idempotencyKey } }, mutation: { onSuccess: useInvalidate(orgId) } })
}
export function useVerifyRun(idempotencyKey: string) {
  return useVerifyBackup({ request: { headers: { 'Idempotency-Key': idempotencyKey } } })
}
export function usePruneRun(orgId: string, idempotencyKey: string) {
  return usePruneBackups({ request: { headers: { 'Idempotency-Key': idempotencyKey } }, mutation: { onSuccess: useInvalidate(orgId) } })
}
