import { describe, expect, it } from 'vitest'
import { ApiProblem, ok, toApiProblem } from '@/api/problem'


describe('problem', () => {
  it('ok returns the data of a successful response', () => {
    expect(ok({ data: { a: 1 }, status: 200 as const })).toEqual({ a: 1 })
  })

  it('builds an ApiProblem with field errors and current', () => {
    const p = toApiProblem(
      {
        type: 'x',
        title: 't',
        status: 409,
        code: 'revision_conflict',
        detail: 'changed',
        errors: [
          { field: 'slug', message: 'already used' },
          { field: 'slug', message: 'ignored' },
        ],
        current: { revision: 2 },
        request_id: 'r1',
      },
      409,
    )
    expect(p).toBeInstanceOf(ApiProblem)
    expect(p.status).toBe(409)
    expect(p.code).toBe('revision_conflict')
    expect(p.current).toEqual({ revision: 2 })
    expect(p.requestId).toBe('r1')
    expect(p.fieldErrors()).toEqual({ slug: 'already used' })
  })

  it('falls back for non-problem bodies', () => {
    const p = toApiProblem('bad gateway', 502)
    expect(p.status).toBe(502)
    expect(p.code).toBe('unknown')
    expect(p.detail).toBe('bad gateway')
    expect(toApiProblem(undefined, 0).code).toBe('network_error')
  })
})
