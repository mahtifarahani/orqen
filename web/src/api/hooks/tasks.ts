import { useQueryClient } from '@tanstack/react-query'
import { useGetProjectIntake, useEvaluateTask as useEvaluateMutation, useGetTask, useGetTaskSummary, useListTasksInfinite, usePollProjectNow as usePollMutation } from '../generated/tasks/tasks'
import {
  useCancelRun as useCancelMutation,
  useGetRun,
  useGetRoleContext,
  useGetRunContext,
  useListRunTimeline,
  useResumeRun as useResumeMutation,
  useRetryRun as useRetryMutation,
  useStartRun as useStartMutation,
} from '../generated/runs/runs'
import { useDiscardWorkspace as useDiscardMutation } from '../generated/workspaces/workspaces'
import type { RoleName, TaskSummaryDto } from '../generated/model'
import { ifMatch } from '../mutator'
import { invalidatePath } from '../query-keys'
import { ok } from '../problem'

export type TaskSummary = TaskSummaryDto
const PAGE = 50

export interface TaskFilters {
  projectId?: string
  state?: string
  q?: string
}

export function useTasks(orgId: string, f: TaskFilters) {
  return useListTasksInfinite(
    orgId,
    { limit: PAGE, ...(f.projectId ? { project_id: f.projectId } : {}), ...(f.state ? { state: f.state } : {}), ...(f.q ? { q: f.q } : {}) },
    { query: { initialPageParam: undefined, getNextPageParam: (last) => ok(last).next_cursor ?? undefined } },
  )
}
export function taskRows(data: ReturnType<typeof useTasks>['data']): TaskSummary[] {
  return data?.pages.flatMap((p) => ok(p).items) ?? []
}
export function useTask(orgId: string, taskId: string) {
  return useGetTask(orgId, taskId, { query: { select: (res) => ok(res) } })
}
export function useRun(orgId: string, runId: string | undefined | null) {
  return useGetRun(orgId, runId ?? '', { query: { enabled: !!runId, select: (res) => ok(res) } })
}
export function useRunContext(orgId: string, runId: string | undefined | null, enabled: boolean) {
  return useGetRunContext(orgId, runId ?? '', { query: { enabled: enabled && !!runId, retry: false, select: (res) => ok(res) } })
}
export function useRoleContext(orgId: string, runId: string, role: string, enabled: boolean) {
  return useGetRoleContext(orgId, runId, role as RoleName, { query: { enabled, retry: false, select: (res) => ok(res) } })
}
export function useRunTimeline(orgId: string, runId: string | undefined | null) {
  return useListRunTimeline(orgId, runId ?? '', { limit: 200 }, { query: { enabled: !!runId, select: (res) => ok(res).items } })
}
export function useTaskSummary(orgId: string) {
  return useGetTaskSummary(orgId, { query: { select: (res) => ok(res) } })
}
export function useProjectIntake(orgId: string, projectId: string) {
  return useGetProjectIntake(orgId, projectId, { query: { select: (res) => ok(res) } })
}

function useInvalidateTasks(orgId: string) {
  const qc = useQueryClient()
  return () => invalidatePath(qc, (p) => p.startsWith(`/api/v1/orgs/${orgId}/tasks`) || p.startsWith(`/api/v1/orgs/${orgId}/runs`) || p.startsWith(`/api/v1/orgs/${orgId}/inbox`))
}

/** One Idempotency-Key per user intent: the caller keeps it on failure (a retry replays) and renews it on success. */
export function useEvaluateTask(orgId: string, idempotencyKey: string) {
  const invalidate = useInvalidateTasks(orgId)
  return useEvaluateMutation({ request: { headers: { 'Idempotency-Key': idempotencyKey } }, mutation: { onSuccess: invalidate } })
}
export function usePollNow(idempotencyKey: string) {
  return usePollMutation({ request: { headers: { 'Idempotency-Key': idempotencyKey } } })
}
interface RunHeaders {
  revision: number
  idempotencyKey: string
}
const runHeaders = (h: RunHeaders) => ({ headers: { 'If-Match': ifMatch(h.revision), 'Idempotency-Key': h.idempotencyKey } })
export function useStartRun(orgId: string, h: RunHeaders) {
  const invalidate = useInvalidateTasks(orgId)
  return useStartMutation({ request: runHeaders(h), mutation: { onSuccess: invalidate } })
}
export function useResumeRun(orgId: string, h: RunHeaders) {
  const invalidate = useInvalidateTasks(orgId)
  return useResumeMutation({ request: runHeaders(h), mutation: { onSuccess: invalidate } })
}
export function useCancelRun(orgId: string, h: RunHeaders) {
  const invalidate = useInvalidateTasks(orgId)
  return useCancelMutation({ request: runHeaders(h), mutation: { onSuccess: invalidate } })
}
export function useRetryRun(orgId: string, h: RunHeaders) {
  const invalidate = useInvalidateTasks(orgId)
  return useRetryMutation({ request: runHeaders(h), mutation: { onSuccess: invalidate } })
}
export function useDiscardWorkspace(orgId: string, h: RunHeaders) {
  const invalidate = useInvalidateTasks(orgId)
  return useDiscardMutation({ request: runHeaders(h), mutation: { onSuccess: invalidate } })
}
