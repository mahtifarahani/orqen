import { useQueryClient } from '@tanstack/react-query'
import {
  useApproveSkillVersion,
  useBindRecommendedSkills,
  useGetRunSkills,
  useGetSkill,
  useGetSkillBindings,
  useGetSkillVersion,
  useImportSkill,
  useListSkills,
  usePutSkillBindings,
  useResolveSkillBindings,
  useRetireSkillVersion,
} from '../generated/skills/skills'
import { ifMatch } from '../mutator'
import { invalidatePath } from '../query-keys'
import { ok } from '../problem'

const base = (orgId: string) => `/api/v1/orgs/${orgId}/skill`

export function useSkills(orgId: string) {
  return useListSkills(orgId, { query: { select: (res) => ok(res).items } })
}
export function useSkill(orgId: string, id: string | null) {
  return useGetSkill(orgId, id ?? '', { query: { enabled: !!id, select: (res) => ok(res) } })
}
export function useSkillVersion(orgId: string, id: string | null) {
  return useGetSkillVersion(orgId, id ?? '', { query: { enabled: !!id, select: (res) => ok(res) } })
}
export function useSkillBindings(orgId: string, scopeKey: string) {
  return useGetSkillBindings(orgId, { scope_key: scopeKey }, { query: { select: (res) => ok(res) } })
}
export function useResolvedSkills(orgId: string, projectId: string | undefined, role: string, round = 0) {
  return useResolveSkillBindings(orgId, { project_id: projectId, role, round }, { query: { select: (res) => ok(res) } })
}
export function useRunSkills(orgId: string, runId: string) {
  return useGetRunSkills(orgId, runId, { query: { select: (res) => ok(res) } })
}

function useInvalidate(orgId: string) {
  const qc = useQueryClient()
  return () => invalidatePath(qc, (p) => p.startsWith(base(orgId)) || p.startsWith(`/api/v1/orgs/${orgId}/runs`))
}
export function useImportSkillRun(orgId: string, key: string) {
  const invalidate = useInvalidate(orgId)
  return useImportSkill({ request: { headers: { 'Idempotency-Key': key } }, mutation: { onSuccess: invalidate } })
}
export function useApproveVersion(orgId: string, revision: number) {
  const invalidate = useInvalidate(orgId)
  return useApproveSkillVersion({ request: { headers: { 'If-Match': ifMatch(revision) } }, mutation: { onSuccess: invalidate } })
}
export function useRetireVersion(orgId: string, revision: number) {
  const invalidate = useInvalidate(orgId)
  return useRetireSkillVersion({ request: { headers: { 'If-Match': ifMatch(revision) } }, mutation: { onSuccess: invalidate } })
}
export function usePutBindings(orgId: string, revision: number) {
  const invalidate = useInvalidate(orgId)
  return usePutSkillBindings({ request: { headers: { 'If-Match': ifMatch(revision) } }, mutation: { onSuccess: invalidate } })
}
export function useRecommendedBindings(orgId: string, key: string) {
  const invalidate = useInvalidate(orgId)
  return useBindRecommendedSkills({ request: { headers: { 'Idempotency-Key': key } }, mutation: { onSuccess: invalidate } })
}
