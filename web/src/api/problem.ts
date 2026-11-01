import type { FieldError, Problem } from './generated/model'

/** RFC 9457 problem (`application/problem+json`) raised as an exception. */
export class ApiProblem extends Error {
  status: number
  code: string
  detail: string
  errors?: FieldError[]
  current?: unknown
  requestId?: string

  constructor(init: {
    status: number
    code: string
    detail: string
    errors?: FieldError[] | null
    current?: unknown
    requestId?: string | null
  }) {
    super(init.detail || init.code)
    this.name = 'ApiProblem'
    this.status = init.status
    this.code = init.code
    this.detail = init.detail
    this.errors = init.errors ?? undefined
    this.current = init.current ?? undefined
    this.requestId = init.requestId ?? undefined
  }

  /** Field-level messages keyed by field name (first message wins). */
  fieldErrors(): Record<string, string> {
    const out: Record<string, string> = {}
    for (const e of this.errors ?? []) out[e.field] ??= e.message
    return out
  }
}

function isProblem(x: unknown): x is Partial<Problem> {
  return typeof x === 'object' && x !== null && 'code' in x
}

export function toApiProblem(error: unknown, status: number): ApiProblem {
  if (isProblem(error)) {
    return new ApiProblem({
      status: error.status ?? status,
      code: String(error.code),
      detail: error.detail ?? error.title ?? '',
      errors: error.errors,
      current: error.current,
      requestId: error.request_id,
    })
  }
  return new ApiProblem({
    status,
    code: status === 0 ? 'network_error' : 'unknown',
    detail: typeof error === 'string' ? error : '',
  })
}

type SuccessStatus = 200 | 201 | 202 | 204

/**
 * Generated hooks resolve to `{data, status, headers}` (a union with the problem branch, although
 * the mutator throws on errors). This narrows to the successful branch's `data`.
 */
export function ok<R extends { status: number; data?: unknown }>(res: R): Extract<R, { status: SuccessStatus }>['data'] {
  return res.data as Extract<R, { status: SuccessStatus }>['data']
}

export const isProblem409 = (e: unknown): e is ApiProblem => e instanceof ApiProblem && e.code === 'revision_conflict'
