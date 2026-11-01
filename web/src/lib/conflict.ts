import { ApiProblem } from '@/api/problem'

/** The server's current document when a save lost the If-Match race (409 revision_conflict), else null. */
export function conflictCurrent<T>(error: unknown): T | null {
  if (error instanceof ApiProblem && error.code === 'revision_conflict' && error.current) return error.current as T
  return null
}

/** `a` and `b` have the same JSON value (key order of objects is ignored). */
export function sameJson(a: unknown, b: unknown): boolean {
  return stable(a) === stable(b)
}
function stable(v: unknown): string {
  return JSON.stringify(v, (_k, val: unknown) =>
    val && typeof val === 'object' && !Array.isArray(val)
      ? Object.fromEntries(Object.entries(val as Record<string, unknown>).sort(([x], [y]) => (x < y ? -1 : 1)))
      : val,
  )
}
