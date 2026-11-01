import { toApiProblem, ApiProblem } from './problem'

let csrf: string | null = null
export const setCsrf = (t: string | null) => {
  csrf = t
}
export const getCsrf = () => csrf

/** `If-Match` value for a resource revision. */
export const ifMatch = (revision: number) => `"${revision}"`

/**
 * orval fetch mutator: same-origin credentials, X-CSRF-Token on unsafe methods, problem+json to
 * `ApiProblem` (thrown, so TanStack Query sees an error), response headers kept (ETag,
 * Idempotent-Replayed).
 */
export const orqenFetch = async <T>(url: string, init: RequestInit = {}): Promise<T> => {
  const method = (init.method ?? 'GET').toUpperCase()
  const headers = new Headers(init.headers)
  if (method !== 'GET' && method !== 'HEAD' && csrf) headers.set('X-CSRF-Token', csrf)

  let res: Response
  try {
    res = await fetch(url, { ...init, headers, credentials: 'same-origin' })
  } catch (e) {
    if (e instanceof DOMException && e.name === 'AbortError') throw e
    throw new ApiProblem({ status: 0, code: 'network_error', detail: '' })
  }

  const text = res.status === 204 ? '' : await res.text()
  let data: unknown = undefined
  if (text) {
    const ct = res.headers.get('content-type') ?? ''
    if (ct.includes('json')) {
      try {
        data = JSON.parse(text)
      } catch {
        data = text
      }
    } else {
      data = text
    }
  }
  if (!res.ok) throw toApiProblem(data, res.status)
  return { data, status: res.status, headers: res.headers } as T
}
