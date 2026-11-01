import { useQueryClient } from '@tanstack/react-query'
import {
  useListKnowledge,
  useGetKnowledge,
  useListKnowledgeVersions,
  useListKnowledgeConflicts,
  useRejectKnowledgeVersion as useRejectMutation,
  useResolveKnowledgeConflict as useResolveMutation,
  useRollbackKnowledge as useRollbackMutation,
  useArchiveKnowledge as useArchiveMutation,
  useVerifyKnowledgeVersion as useVerifyMutation,
  useSearchKnowledge,
} from '../generated/knowledge/knowledge'
import {
  useCancelKnowledgeAuthorSession,
  useFallbackKnowledgeAuthorSession,
  useGetWikiStatus,
  useListKnowledgeAuthorSessions,
  useListKnowledgeProposals,
  useRetryKnowledgeAuthorSession,
} from '../generated/knowledge/knowledge'
import type { ListKnowledgeParams } from '../generated/model'
import { ifMatch } from '../mutator'
import { invalidatePath } from '../query-keys'
import { ok } from '../problem'

export function useKnowledgeList(orgId: string, params: ListKnowledgeParams = {}) {
  return useListKnowledge(orgId, params, { query: { select: (res) => ok(res) } })
}
export function useKnowledgeRecord(orgId: string, id: string | undefined) {
  return useGetKnowledge(orgId, id ?? '', { query: { enabled: !!id, select: (res) => ok(res) } })
}
export function useKnowledgeVersionList(orgId: string, id: string | undefined) {
  return useListKnowledgeVersions(orgId, id ?? '', { query: { enabled: !!id, select: (res) => ok(res) } })
}
export function useConflicts(orgId: string) {
  return useListKnowledgeConflicts(orgId, { state: 'open' }, { query: { select: (res) => ok(res) } })
}
export function useWikiStatus(orgId: string) {
  return useGetWikiStatus(orgId, { query: { select: (res) => ok(res), refetchInterval: 15000 } })
}
export function useKnowledgeSearch(orgId: string, q: string) {
  return useSearchKnowledge(orgId, { q }, { query: { enabled: q.trim().length > 0, select: (res) => ok(res) } })
}
function useInvalidate(orgId: string) {
  const qc = useQueryClient()
  return () => invalidatePath(qc, (p) => p.startsWith(`/api/v1/orgs/${orgId}/knowledge`) || p.startsWith(`/api/v1/orgs/${orgId}/wiki`))
}
export function useVerifyVersion(orgId: string, revision: number) {
  return useVerifyMutation({ request: { headers: { 'If-Match': ifMatch(revision) } }, mutation: { onSuccess: useInvalidate(orgId) } })
}
export function useRejectVersion(orgId: string, revision: number) {
  return useRejectMutation({ request: { headers: { 'If-Match': ifMatch(revision) } }, mutation: { onSuccess: useInvalidate(orgId) } })
}
export function useRollback(orgId: string, revision: number) {
  return useRollbackMutation({ request: { headers: { 'If-Match': ifMatch(revision) } }, mutation: { onSuccess: useInvalidate(orgId) } })
}
export function useArchiveRecord(orgId: string, revision: number) {
  return useArchiveMutation({ request: { headers: { 'If-Match': ifMatch(revision) } }, mutation: { onSuccess: useInvalidate(orgId) } })
}
export function useResolveConflict(orgId: string, revision: number) {
  return useResolveMutation({ request: { headers: { 'If-Match': ifMatch(revision) } }, mutation: { onSuccess: useInvalidate(orgId) } })
}

/** Roles-author sessions (S7). Active ones poll as well, since the server also pushes knowledge.author_session events. */
export function useAuthorSessions(orgId: string) {
  return useListKnowledgeAuthorSessions(orgId, {}, { query: { select: (res) => ok(res), refetchInterval: 15000 } })
}
export function useProposals(orgId: string) {
  return useListKnowledgeProposals(orgId, {}, { query: { select: (res) => ok(res) } })
}
/** Cancel carries the revision the user saw (If-Match). */
export function useCancelAuthorSession(orgId: string, revision: number) {
  return useCancelKnowledgeAuthorSession({ request: { headers: { 'If-Match': ifMatch(revision) } }, mutation: { onSuccess: useInvalidate(orgId) } })
}
/** Retry and fallback are idempotent: the caller owns one Idempotency-Key per user intent. */
export function useRetryAuthorSession(orgId: string, idempotencyKey: string) {
  return useRetryKnowledgeAuthorSession({ request: { headers: { 'Idempotency-Key': idempotencyKey } }, mutation: { onSuccess: useInvalidate(orgId) } })
}
export function useFallbackAuthorSession(orgId: string, idempotencyKey: string) {
  return useFallbackKnowledgeAuthorSession({ request: { headers: { 'Idempotency-Key': idempotencyKey } }, mutation: { onSuccess: useInvalidate(orgId) } })
}
