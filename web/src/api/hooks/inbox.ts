import { useQueryClient } from '@tanstack/react-query'
import { useAnswerInboxItem as useAnswerMutation, useGetInboxItem, useListInboxInfinite, useResolveInboxItem as useResolveMutation } from '../generated/inbox/inbox'
import type { InboxItemDto } from '../generated/model'
import { ifMatch } from '../mutator'
import { invalidatePath } from '../query-keys'
import { ok } from '../problem'

export type InboxItem = InboxItemDto
const PAGE = 50

export interface InboxFilters {
  projectId?: string
  priority?: string
  reason?: string
  status?: string
}

export function useInbox(orgId: string, f: InboxFilters) {
  return useListInboxInfinite(
    orgId,
    { limit: PAGE, ...(f.projectId ? { project_id: f.projectId } : {}), ...(f.priority ? { priority: f.priority } : {}), ...(f.reason ? { reason: f.reason } : {}), ...(f.status ? { status: f.status } : {}) },
    { query: { initialPageParam: undefined, getNextPageParam: (last) => ok(last).next_cursor ?? undefined } },
  )
}
export function inboxRows(data: ReturnType<typeof useInbox>['data']): InboxItem[] {
  return data?.pages.flatMap((p) => ok(p).items) ?? []
}
export function useInboxItem(orgId: string, id: string | null) {
  return useGetInboxItem(orgId, id ?? '', { query: { enabled: !!id, select: (res) => ok(res) } })
}
function useInvalidateInbox(orgId: string) {
  const qc = useQueryClient()
  return () => invalidatePath(qc, (p) => p.startsWith(`/api/v1/orgs/${orgId}/inbox`) || p.startsWith(`/api/v1/orgs/${orgId}/tasks`) || p.startsWith(`/api/v1/orgs/${orgId}/runs`))
}
interface Headers {
  revision: number
  idempotencyKey: string
}
const h = (x: Headers) => ({ headers: { 'If-Match': ifMatch(x.revision), 'Idempotency-Key': x.idempotencyKey } })
export function useAnswerInbox(orgId: string, x: Headers) {
  const invalidate = useInvalidateInbox(orgId)
  return useAnswerMutation({ request: h(x), mutation: { onSuccess: invalidate } })
}
export function useResolveInbox(orgId: string, x: Headers) {
  const invalidate = useInvalidateInbox(orgId)
  return useResolveMutation({ request: h(x), mutation: { onSuccess: invalidate } })
}
