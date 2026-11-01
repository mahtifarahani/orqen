import { useQueryClient } from '@tanstack/react-query'
import {
  useAcceptExtensionLicense,
  useBindProjectExtension,
  useCheckExtensionHealth,
  useDeleteExtensionCredential,
  useDetectExtension,
  useDisableExtension,
  useEnableExtension,
  useFindExtensionCandidates,
  useGetExtension,
  useListExtensions,
  useRegisterExtensionBinary,
  useRevokeExtensionLicense,
  useRollbackExtension,
  useSetExtensionConfig,
  useSetExtensionCredential,
  useTestExtension,
} from '../generated/extensions/extensions'
import type { ExtensionDetailDto, ExtensionSummaryDto } from '../generated/model'
import { ifMatch } from '../mutator'
import { invalidatePath } from '../query-keys'
import { ok } from '../problem'

export type ExtensionSummary = ExtensionSummaryDto
export type ExtensionDetail = ExtensionDetailDto

const base = (orgId: string) => `/api/v1/orgs/${orgId}/extensions`

export function useExtensions(orgId: string) {
  return useListExtensions(orgId, { query: { select: (res) => ok(res).items } })
}
export function useExtension(orgId: string, extId: string | null) {
  return useGetExtension(orgId, extId ?? '', { query: { enabled: !!extId, select: (res) => ok(res) } })
}
export function useExtensionCandidates(orgId: string, extId: string, enabled: boolean) {
  return useFindExtensionCandidates(orgId, extId, { query: { enabled, select: (res) => ok(res) } })
}

function useInvalidate(orgId: string) {
  const qc = useQueryClient()
  return () => invalidatePath(qc, (p) => p.startsWith(base(orgId)) || p.startsWith(`/api/v1/orgs/${orgId}/security/status`) || p.startsWith(`/api/v1/orgs/${orgId}/graph/status`))
}

/** Every mutating action of one extension, each bound to the revision the sheet is showing (If-Match). */
export function useExtensionActions(orgId: string, revision: number) {
  const invalidate = useInvalidate(orgId)
  const request = { headers: { 'If-Match': ifMatch(revision) } }
  const mutation = { onSuccess: invalidate }
  return {
    register: useRegisterExtensionBinary({ request, mutation }),
    acceptLicense: useAcceptExtensionLicense({ request, mutation }),
    revokeLicense: useRevokeExtensionLicense({ request, mutation }),
    setConfig: useSetExtensionConfig({ request, mutation }),
    enable: useEnableExtension({ request, mutation }),
    disable: useDisableExtension({ request, mutation }),
    rollback: useRollbackExtension({ request, mutation }),
    // credentials carry no precondition and never come back from the server
    setCredential: useSetExtensionCredential({ mutation }),
    deleteCredential: useDeleteExtensionCredential({ mutation }),
  }
}

/** detect, test and health are operations; the caller owns the Idempotency-Key (see useOperationRunner). */
export function useDetectRun(orgId: string, key: string) {
  const invalidate = useInvalidate(orgId)
  return useDetectExtension({ request: { headers: { 'Idempotency-Key': key } }, mutation: { onSuccess: invalidate } })
}
export function useTestRun(orgId: string, key: string) {
  const invalidate = useInvalidate(orgId)
  return useTestExtension({ request: { headers: { 'Idempotency-Key': key } }, mutation: { onSuccess: invalidate } })
}
export function useHealthRun(orgId: string, key: string) {
  const invalidate = useInvalidate(orgId)
  return useCheckExtensionHealth({ request: { headers: { 'Idempotency-Key': key } }, mutation: { onSuccess: invalidate } })
}

/** Per project enablement of an extension; If-Match is the binding revision (0 while no binding exists). */
export function useBindProject(orgId: string, revision: number) {
  const invalidate = useInvalidate(orgId)
  return useBindProjectExtension({ request: { headers: { 'If-Match': ifMatch(revision) } }, mutation: { onSuccess: invalidate } })
}
