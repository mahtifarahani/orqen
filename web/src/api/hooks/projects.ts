import { useQueryClient } from '@tanstack/react-query'
import {
  useDisableAutomation as useDisableMutation,
  useEnableAutomation as useEnableMutation,
  useFromCatalog as useFromCatalogMutation,
  useGetContractSnapshot,
  useGetProject,
  useGetProjectReadiness,
  useListContractSnapshots,
  useListProjects,
  useListProjectsInfinite,
  usePutCommands as usePutCommandsMutation,
  usePutMapping as usePutMappingMutation,
  usePutProfileBinding as usePutBindingMutation,
  usePutProtectedPaths as usePutProtectedMutation,
  useReviewContract as useReviewMutation,
  useStartContractImport as useContractImportMutation,
  useStartMappingCheck as useMappingCheckMutation,
} from '../generated/projects/projects'
import { useGetArtifactContent } from '../generated/artifacts/artifacts'
import type { ProjectDto, ProjectSummaryDto } from '../generated/model'
import { ifMatch } from '../mutator'
import { invalidatePath } from '../query-keys'
import { ok } from '../problem'

export type Project = ProjectDto
export type ProjectSummary = ProjectSummaryDto

const PAGE = 50

export function useProjects(orgId: string, search: string) {
  return useListProjectsInfinite(
    orgId,
    { limit: PAGE, ...(search ? { search } : {}) },
    {
      query: {
        initialPageParam: undefined,
        getNextPageParam: (last) => ok(last).next_cursor ?? undefined,
      },
    },
  )
}

export function projectRows(data: ReturnType<typeof useProjects>['data']): ProjectSummary[] {
  return data?.pages.flatMap((p) => ok(p).items) ?? []
}

export function useProject(orgId: string, projectId: string) {
  return useGetProject(orgId, projectId, { query: { select: (res) => ok(res) } })
}

export function useProjectReadiness(orgId: string, projectId: string) {
  return useGetProjectReadiness(orgId, projectId, { query: { select: (res) => ok(res) } })
}

/** One Idempotency-Key per "Add" intent; the caller renews it after a success or when the selection changes. */
export function useAddFromCatalog(orgId: string, idempotencyKey: string) {
  const qc = useQueryClient()
  return useFromCatalogMutation({
    request: { headers: { 'Idempotency-Key': idempotencyKey } },
    mutation: {
      onSuccess: () => invalidatePath(qc, (p) => p.startsWith(`/api/v1/orgs/${orgId}/projects`) || (p.startsWith(`/api/v1/orgs/${orgId}/connections`) && p.includes('/catalog-projects'))),
    },
  })
}

/** Project data, readiness and the policy tabs all derive from the project; refresh them together. */
function useInvalidateProject(orgId: string) {
  const qc = useQueryClient()
  return () => invalidatePath(qc, (p) => p.startsWith(`/api/v1/orgs/${orgId}/projects`))
}

export function usePutMapping(orgId: string, revision: number) {
  const invalidate = useInvalidateProject(orgId)
  return usePutMappingMutation({ request: { headers: { 'If-Match': ifMatch(revision) } }, mutation: { onSuccess: invalidate } })
}
export function usePutCommands(orgId: string, revision: number) {
  const invalidate = useInvalidateProject(orgId)
  return usePutCommandsMutation({ request: { headers: { 'If-Match': ifMatch(revision) } }, mutation: { onSuccess: invalidate } })
}
export function usePutProtectedPaths(orgId: string, revision: number) {
  const invalidate = useInvalidateProject(orgId)
  return usePutProtectedMutation({ request: { headers: { 'If-Match': ifMatch(revision) } }, mutation: { onSuccess: invalidate } })
}
export function useReviewContract(orgId: string, revision: number) {
  const invalidate = useInvalidateProject(orgId)
  return useReviewMutation({ request: { headers: { 'If-Match': ifMatch(revision) } }, mutation: { onSuccess: invalidate } })
}
export function usePutProfileBinding(orgId: string, revision: number) {
  const invalidate = useInvalidateProject(orgId)
  return usePutBindingMutation({ request: { headers: { 'If-Match': ifMatch(revision) } }, mutation: { onSuccess: invalidate } })
}
export function useEnableAutomation(orgId: string, revision: number, idempotencyKey: string) {
  const invalidate = useInvalidateProject(orgId)
  return useEnableMutation({
    request: { headers: { 'If-Match': ifMatch(revision), 'Idempotency-Key': idempotencyKey } },
    mutation: { onSuccess: invalidate },
  })
}
export function useDisableAutomation(orgId: string, revision: number) {
  const invalidate = useInvalidateProject(orgId)
  return useDisableMutation({ request: { headers: { 'If-Match': ifMatch(revision) } }, mutation: { onSuccess: invalidate } })
}
/** Operations: the caller owns the Idempotency-Key (see useOperationRunner). */
export function useStartMappingCheck(idempotencyKey: string) {
  return useMappingCheckMutation({ request: { headers: { 'Idempotency-Key': idempotencyKey } } })
}
export function useStartContractImport(idempotencyKey: string) {
  return useContractImportMutation({ request: { headers: { 'Idempotency-Key': idempotencyKey } } })
}

export function useContractSnapshots(orgId: string, projectId: string) {
  return useListContractSnapshots(orgId, projectId, { limit: 20 }, { query: { select: (res) => ok(res).items } })
}
export function useContractSnapshot(orgId: string, projectId: string, rcsId: string | undefined | null) {
  return useGetContractSnapshot(orgId, projectId, rcsId ?? '', { query: { enabled: !!rcsId, select: (res) => ok(res) } })
}
/** File content of a contract snapshot (an artifact); text. */
export function useArtifactText(orgId: string, artifactId: string | undefined) {
  return useGetArtifactContent(orgId, artifactId ?? '', { query: { enabled: !!artifactId, select: (res) => res.data as unknown as string } })
}

/** One page of up to 200 projects, enough for the Overview counters. */
export function useProjectCounts(orgId: string) {
  return useListProjects(orgId, { limit: 200 }, { query: { select: (res) => ok(res) } })
}
