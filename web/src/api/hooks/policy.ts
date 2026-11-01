import { useQueryClient } from '@tanstack/react-query'
import {
  useGetPolicyDraft,
  useGetPolicyEffective,
  useGetPolicySchema,
  useGetProjectPolicyDraft,
  useGetProjectPolicyEffective,
  useListPolicyVersions,
  useListProjectPolicyVersions,
  usePublishPolicy as usePublishOrgMutation,
  usePublishProjectPolicy as usePublishProjectMutation,
  usePutPolicyDraft as usePutOrgDraftMutation,
  usePutProjectPolicyDraft as usePutProjectDraftMutation,
} from '../generated/policy/policy'
import { ifMatch } from '../mutator'
import { invalidatePath } from '../query-keys'
import { ok } from '../problem'

/** The same call shape for the org and the project scope. `A` are the arguments before the mutate options. */
export interface Writer<A extends unknown[]> {
  run: (...args: [...A, WriteOptions?]) => void
  isPending: boolean
  error: unknown
  reset: () => void
}
export interface WriteOptions {
  onSuccess?: (res: { data: unknown }) => void
  onError?: (err: unknown) => void
}

export function usePolicySchema(orgId: string) {
  return useGetPolicySchema(orgId, { query: { select: (res) => ok(res), staleTime: 60_000 } })
}

/** Both scopes expose the same shape so one editor can render either. */
export function useOrgPolicy(orgId: string) {
  const qc = useQueryClient()
  const invalidate = () => invalidatePath(qc, (p) => p.startsWith(`/api/v1/orgs/${orgId}/policy`) || p.startsWith(`/api/v1/orgs/${orgId}/projects`))
  return {
    draft: useGetPolicyDraft(orgId, { query: { select: (res) => ok(res) } }),
    effective: useGetPolicyEffective(orgId, { query: { select: (res) => ok(res) } }),
    versions: useListPolicyVersions(orgId, { limit: 20 }, { query: { select: (res) => ok(res).items } }),
    useSave: (revision: number): Writer<[Record<string, unknown>]> => {
      const m = usePutOrgDraftMutation({ request: { headers: { 'If-Match': ifMatch(revision) } }, mutation: { onSuccess: invalidate } })
      return { run: (overrides, o) => m.mutate({ orgId, data: { overrides } }, o), isPending: m.isPending, error: m.error, reset: m.reset }
    },
    usePublish: (revision: number, idempotencyKey: string): Writer<[]> => {
      const m = usePublishOrgMutation({ request: { headers: { 'If-Match': ifMatch(revision), 'Idempotency-Key': idempotencyKey } }, mutation: { onSuccess: invalidate } })
      return { run: (o) => m.mutate({ orgId }, o), isPending: m.isPending, error: m.error, reset: m.reset }
    },
  }
}

export function useProjectPolicy(orgId: string, projectId: string) {
  const qc = useQueryClient()
  const invalidate = () => invalidatePath(qc, (p) => p.startsWith(`/api/v1/orgs/${orgId}/projects`))
  return {
    draft: useGetProjectPolicyDraft(orgId, projectId, { query: { select: (res) => ok(res) } }),
    effective: useGetProjectPolicyEffective(orgId, projectId, { query: { select: (res) => ok(res) } }),
    versions: useListProjectPolicyVersions(orgId, projectId, { limit: 20 }, { query: { select: (res) => ok(res).items } }),
    useSave: (revision: number): Writer<[Record<string, unknown>]> => {
      const m = usePutProjectDraftMutation({ request: { headers: { 'If-Match': ifMatch(revision) } }, mutation: { onSuccess: invalidate } })
      return { run: (overrides, o) => m.mutate({ orgId, prjId: projectId, data: { overrides } }, o), isPending: m.isPending, error: m.error, reset: m.reset }
    },
    usePublish: (revision: number, idempotencyKey: string): Writer<[]> => {
      const m = usePublishProjectMutation({ request: { headers: { 'If-Match': ifMatch(revision), 'Idempotency-Key': idempotencyKey } }, mutation: { onSuccess: invalidate } })
      return { run: (o) => m.mutate({ orgId, prjId: projectId }, o), isPending: m.isPending, error: m.error, reset: m.reset }
    },
  }
}

export type PolicyScope = ReturnType<typeof useOrgPolicy> | ReturnType<typeof useProjectPolicy>

/** The effective org policy alone (the host card only needs one key and its source). */
export function useOrgPolicyEffective(orgId: string) {
  return useGetPolicyEffective(orgId, { query: { select: (res) => ok(res) } })
}
