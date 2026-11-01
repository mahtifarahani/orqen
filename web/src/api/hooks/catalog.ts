import {
  useCatalogSummary as useCatalogSummaryQuery,
  useListCatalogProjectsInfinite,
  useRefreshCatalog as useRefreshMutation,
} from '../generated/catalog/catalog'
import type { CatalogProjectDto, ListCatalogProjectsParams } from '../generated/model'
import { ok } from '../problem'

export type CatalogProject = CatalogProjectDto
export type CatalogFilter = Omit<ListCatalogProjectsParams, 'cursor' | 'limit'>

const PAGE = 50

/** Catalog rows of one connection, keyset-paged with "load more". */
export function useCatalogProjects(orgId: string, conId: string | undefined, filter: CatalogFilter) {
  return useListCatalogProjectsInfinite(
    orgId,
    conId ?? '',
    { ...filter, limit: PAGE },
    {
      query: {
        enabled: !!conId,
        initialPageParam: undefined,
        getNextPageParam: (last) => ok(last).next_cursor ?? undefined,
      },
    },
  )
}

export function catalogRows(data: ReturnType<typeof useCatalogProjects>['data']): CatalogProject[] {
  return data?.pages.flatMap((p) => ok(p).items) ?? []
}

export function useCatalogSummary(orgId: string) {
  return useCatalogSummaryQuery(orgId, { query: { select: (res) => ok(res) } })
}

/** The Idempotency-Key is created once per click intent (see useOperationRunner). */
export function useRefreshCatalog(idempotencyKey: string) {
  return useRefreshMutation({ request: { headers: { 'Idempotency-Key': idempotencyKey } } })
}
