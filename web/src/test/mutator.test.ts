import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiProblem } from '@/api/problem'
import { ifMatch, orqenFetch, setCsrf } from '@/api/mutator'

afterEach(() => {
  vi.unstubAllGlobals()
  setCsrf(null)
})

function stubFetch(res: () => Response) {
  const fn = vi.fn().mockImplementation(() => Promise.resolve(res()))
  vi.stubGlobal('fetch', fn)
  return fn
}

describe('orqenFetch', () => {
  it('adds the CSRF header to unsafe methods only and uses same-origin credentials', async () => {
    setCsrf('tok')
    const fetchMock = stubFetch(() => Response.json({ ok: 1 }))
    await orqenFetch('/api/x', { method: 'GET' })
    await orqenFetch('/api/x', { method: 'POST', headers: { 'If-Match': ifMatch(3) } })
    const get = fetchMock.mock.calls[0][1] as RequestInit
    const post = fetchMock.mock.calls[1][1] as RequestInit
    expect(new Headers(get.headers).has('X-CSRF-Token')).toBe(false)
    expect(new Headers(post.headers).get('X-CSRF-Token')).toBe('tok')
    expect(new Headers(post.headers).get('If-Match')).toBe('"3"')
    expect(post.credentials).toBe('same-origin')
  })

  it('returns data, status and response headers', async () => {
    stubFetch(() =>
      Response.json({ id: 'a' }, { status: 201, headers: { ETag: '"1"', 'Idempotent-Replayed': 'true' } }),
    )
    const r = await orqenFetch<{ data: { id: string }; status: number; headers: Headers }>('/api/x', { method: 'POST' })
    expect(r.status).toBe(201)
    expect(r.data).toEqual({ id: 'a' })
    expect(r.headers.get('etag')).toBe('"1"')
    expect(r.headers.get('idempotent-replayed')).toBe('true')
  })

  it('handles 204 without a body', async () => {
    stubFetch(() => new Response(null, { status: 204 }))
    const r = await orqenFetch<{ data: undefined; status: number }>('/api/x', { method: 'DELETE' })
    expect(r.status).toBe(204)
    expect(r.data).toBeUndefined()
  })

  it('preserves JSON artifact source text, including formatting', async () => {
    const source = '{\n  "name": "hamrahtel-website",\n  "scripts": { "test": "vitest run" }\n}\n'
    const fetchMock = stubFetch(() => new Response(source, { headers: { 'content-type': 'application/json' } }))
    const r = await orqenFetch<{ data: string }>('/api/artifacts/package/content', { responseType: 'text' })
    expect(r.data).toBe(source)
    expect(fetchMock.mock.calls[0][1]).not.toHaveProperty('responseType')
  })

  it('still parses structured errors when reading artifact text', async () => {
    stubFetch(() => Response.json({ code: 'not_found', status: 404 }, { status: 404 }))
    await expect(orqenFetch('/api/artifacts/missing/content', { responseType: 'text' })).rejects.toMatchObject({ name: 'ApiProblem', code: 'not_found' })
  })

  it('throws ApiProblem for problem+json errors', async () => {
    stubFetch(
      () =>
        new Response(JSON.stringify({ code: 'revision_conflict', detail: 'x', status: 409, current: { revision: 5 } }), {
          status: 409,
          headers: { 'content-type': 'application/problem+json' },
        }),
    )
    await expect(orqenFetch('/api/x', { method: 'PATCH' })).rejects.toMatchObject({
      name: 'ApiProblem',
      code: 'revision_conflict',
      current: { revision: 5 },
    })
  })

  it('maps a failed fetch to network_error', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('failed')))
    const err = await orqenFetch('/api/x').catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiProblem)
    expect((err as ApiProblem).code).toBe('network_error')
  })
})
