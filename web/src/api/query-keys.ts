import type { Query, QueryClient } from '@tanstack/react-query'

/** Orval query keys start with the request path; infinite queries put 'infinite' in front of it. */
export function queryPath(key: readonly unknown[]): string | undefined {
  const first = key[0] === 'infinite' ? key[1] : key[0]
  return typeof first === 'string' ? first : undefined
}

export function invalidatePath(qc: QueryClient, match: (path: string) => boolean) {
  void qc.invalidateQueries({
    predicate: (q: Query) => {
      const p = queryPath(q.queryKey)
      return p !== undefined && match(p)
    },
  })
}
