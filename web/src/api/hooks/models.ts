import { useQueryClient } from '@tanstack/react-query'
import {
  useArchiveProfile as useArchiveMutation,
  useCreateProfile as useCreateMutation,
  useGetDraftIssues,
  useGetProfile,
  useGetProfileDiff,
  useGetProfileVersion,
  useListProfiles,
  useListProfileVersions,
  usePatchProfile as usePatchMutation,
  usePublishProfile as usePublishMutation,
  usePutProfileDraft as usePutDraftMutation,
  useRollbackProfile as useRollbackMutation,
  useStartImportPersonal as useImportMutation,
  useStartProfileValidation as useValidateMutation,
} from '../generated/model-profiles/model-profiles'
import type { ModelProfileDto } from '../generated/model'
import { ifMatch } from '../mutator'
import { invalidatePath } from '../query-keys'
import { ok } from '../problem'

export type ModelProfile = ModelProfileDto

function useInvalidateModels(orgId: string) {
  const qc = useQueryClient()
  return () => invalidatePath(qc, (p) => p.startsWith(`/api/v1/orgs/${orgId}/model-profile`))
}

export function useProfiles(orgId: string) {
  return useListProfiles(orgId, { limit: 200 }, { query: { select: (res) => ok(res).items } })
}
export function useProfile(orgId: string, profileId: string) {
  return useGetProfile(orgId, profileId, { query: { select: (res) => ok(res) } })
}
export function useDraftIssuesQuery(orgId: string, profileId: string, draftHash: string) {
  // The hash is part of the key so a saved draft always re-reads its Orqen-side issues.
  return useGetDraftIssues(orgId, profileId, { query: { select: (res) => ok(res), queryKey: [`/api/v1/orgs/${orgId}/model-profiles/${profileId}/draft/issues`, draftHash] } })
}
export function useProfileVersions(orgId: string, profileId: string | undefined) {
  return useListProfileVersions(orgId, profileId ?? '', { limit: 50 }, { query: { enabled: !!profileId, select: (res) => ok(res).items } })
}
export function useProfileVersion(orgId: string, versionId: string | undefined | null) {
  return useGetProfileVersion(orgId, versionId ?? '', { query: { enabled: !!versionId, select: (res) => ok(res) } })
}
export function useProfileDiffQuery(orgId: string, profileId: string, from: string | undefined, to: string | undefined) {
  return useGetProfileDiff(orgId, profileId, { ...(from ? { from } : {}), ...(to ? { to } : {}) }, { query: { enabled: !!from && !!to, select: (res) => ok(res) } })
}

/** `idempotencyKey` is optional for creation: the caller keeps one per dialog open. */
export function useCreateProfile(orgId: string, idempotencyKey: string) {
  const invalidate = useInvalidateModels(orgId)
  return useCreateMutation({ request: { headers: { 'Idempotency-Key': idempotencyKey } }, mutation: { onSuccess: invalidate } })
}
export function usePatchProfile(orgId: string, revision: number) {
  const invalidate = useInvalidateModels(orgId)
  return usePatchMutation({ request: { headers: { 'If-Match': ifMatch(revision) } }, mutation: { onSuccess: invalidate } })
}
export function useArchiveProfile(orgId: string, revision: number) {
  const invalidate = useInvalidateModels(orgId)
  return useArchiveMutation({ request: { headers: { 'If-Match': ifMatch(revision) } }, mutation: { onSuccess: invalidate } })
}
export function usePutProfileDraft(orgId: string, revision: number) {
  const invalidate = useInvalidateModels(orgId)
  return usePutDraftMutation({ request: { headers: { 'If-Match': ifMatch(revision) } }, mutation: { onSuccess: invalidate } })
}
export function useStartValidation(idempotencyKey: string) {
  return useValidateMutation({ request: { headers: { 'Idempotency-Key': idempotencyKey } } })
}
export function usePublishProfile(revision: number, idempotencyKey: string) {
  return usePublishMutation({ request: { headers: { 'If-Match': ifMatch(revision), 'Idempotency-Key': idempotencyKey } } })
}
export function useRollbackProfile(revision: number, idempotencyKey: string) {
  return useRollbackMutation({ request: { headers: { 'If-Match': ifMatch(revision), 'Idempotency-Key': idempotencyKey } } })
}
export function useStartImport(idempotencyKey: string) {
  return useImportMutation({ request: { headers: { 'Idempotency-Key': idempotencyKey } } })
}
