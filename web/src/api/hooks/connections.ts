import { useQueryClient } from '@tanstack/react-query'
import {
  useArchiveConnection as useArchiveMutation,
  useCreateConnection as useCreateMutation,
  useDeleteCredential as useDeleteCredentialMutation,
  useListConnections,
  useLookupGitlabBranches,
  useLookupJiraBoards,
  useLookupJiraProjects,
  usePatchConnection as usePatchMutation,
  usePutCredential as usePutCredentialMutation,
  useTestConnection as useTestMutation,
} from '../generated/connections/connections'
import { useGetOperation } from '../generated/operations/operations'
import type { ConnectionDto } from '../generated/model'
import { ifMatch } from '../mutator'
import { invalidatePath } from '../query-keys'
import { ok } from '../problem'

export type Connection = ConnectionDto

/** Orval query keys start with the request path. */
function useInvalidateConnections(orgId: string) {
  const qc = useQueryClient()
  return () =>
    invalidatePath(qc, (p) => p.startsWith(`/api/v1/orgs/${orgId}/connections`))
}

export function useConnections(orgId: string) {
  return useListConnections(orgId, { limit: 200 }, { query: { select: (res) => ok(res).items } })
}

export function useCreateConnection(orgId: string, idempotencyKey: string) {
  const invalidate = useInvalidateConnections(orgId)
  return useCreateMutation({
    request: { headers: { 'Idempotency-Key': idempotencyKey } },
    mutation: { onSuccess: invalidate },
  })
}

export function usePatchConnection(orgId: string, revision: number) {
  const invalidate = useInvalidateConnections(orgId)
  return usePatchMutation({ request: { headers: { 'If-Match': ifMatch(revision) } }, mutation: { onSuccess: invalidate } })
}

export function useArchiveConnection(orgId: string, revision: number) {
  const invalidate = useInvalidateConnections(orgId)
  return useArchiveMutation({ request: { headers: { 'If-Match': ifMatch(revision) } }, mutation: { onSuccess: invalidate } })
}

export function usePutCredential(orgId: string, revision: number) {
  const invalidate = useInvalidateConnections(orgId)
  return usePutCredentialMutation({
    request: { headers: { 'If-Match': ifMatch(revision) } },
    mutation: { onSuccess: invalidate },
  })
}

export function useDeleteCredential(orgId: string, revision: number) {
  const invalidate = useInvalidateConnections(orgId)
  return useDeleteCredentialMutation({
    request: { headers: { 'If-Match': ifMatch(revision) } },
    mutation: { onSuccess: invalidate },
  })
}

/** The Idempotency-Key is created once per click intent by the caller and reused on retry. */
export function useTestConnection(idempotencyKey: string) {
  return useTestMutation({ request: { headers: { 'Idempotency-Key': idempotencyKey } } })
}

export function useOperation(orgId: string, opId: string | null) {
  return useGetOperation(orgId, opId ?? '', {
    query: {
      enabled: !!opId,
      select: (res) => ok(res),
      // SSE invalidates on operation events; polling is only a safety net while it is running.
      refetchInterval: (q) => {
        const s = q.state.data && ok(q.state.data).status
        return s === 'pending' || s === 'running' ? 5000 : false
      },
    },
  })
}

export function useGitlabBranches(orgId: string, conId: string, providerProjectId: string, search: string) {
  return useLookupGitlabBranches(
    orgId,
    conId,
    { provider_project_id: providerProjectId, limit: 50, ...(search ? { search } : {}) },
    { query: { enabled: !!conId && !!providerProjectId, select: (res) => ok(res).items, staleTime: 30_000 } },
  )
}

export function useJiraProjectsLookup(orgId: string, conId: string, query: string) {
  return useLookupJiraProjects(orgId, conId, { limit: 50, ...(query ? { query } : {}) }, { query: { enabled: !!conId, select: (res) => ok(res).items, staleTime: 30_000 } })
}

export function useJiraBoardsLookup(orgId: string, conId: string, projectId: string) {
  return useLookupJiraBoards(orgId, conId, { project_id: projectId, limit: 50 }, { query: { enabled: !!conId && !!projectId, select: (res) => ok(res).items, staleTime: 30_000, retry: false } })
}
