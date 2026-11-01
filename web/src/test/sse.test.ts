import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, renderHook } from '@testing-library/react'
import { createElement, type ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { lastSeqKey, useOrgEvents } from '@/api/sse'

class FakeEventSource {
  static instances: FakeEventSource[] = []
  static CLOSED = 2
  readyState = 0
  onopen: (() => void) | null = null
  onerror: (() => void) | null = null
  onmessage: ((m: MessageEvent<string>) => void) | null = null
  closed = false
  url: string
  constructor(url: string) {
    this.url = url
    FakeEventSource.instances.push(this)
  }
  close() {
    this.closed = true
    this.readyState = 2
  }
  emit(seq: number, type = 'connection.created', operation_id?: string, createdAt = new Date().toISOString()) {
    this.onmessage?.(
      new MessageEvent('message', {
        data: JSON.stringify({ seq, type, actor: 'owner', subject: { kind: 'connection', id: 'con_1' }, payload: {}, created_at: createdAt, operation_id }),
      }),
    )
  }
}

const ORG = 'org_A'
function setup(qc = new QueryClient()) {
  const wrapper = ({ children }: { children: ReactNode }) => createElement(QueryClientProvider, { client: qc }, children)
  return { qc, ...renderHook(() => useOrgEvents(ORG), { wrapper }) }
}

beforeEach(() => {
  FakeEventSource.instances = []
  vi.stubGlobal('EventSource', FakeEventSource)
  sessionStorage.clear()
})
afterEach(() => vi.unstubAllGlobals())

describe('useOrgEvents', () => {
  it('starts at after=0, goes live, buffers events and persists the last seq', () => {
    const { result } = setup()
    const es = FakeEventSource.instances[0]
    expect(es.url).toBe(`/api/v1/orgs/${ORG}/events/stream?after=0`)
    expect(result.current.status).toBe('connecting')
    act(() => es.onopen?.())
    expect(result.current.status).toBe('live')
    act(() => {
      es.emit(1)
      es.emit(2)
    })
    expect(result.current.events.map((e) => e.seq)).toEqual([1, 2])
    expect(sessionStorage.getItem(lastSeqKey(ORG))).toBe('2')
  })

  it('a replayed history invalidates the active queries once, after it settles', () => {
    vi.useFakeTimers()
    try {
      const qc = new QueryClient()
      const spy = vi.spyOn(qc, 'invalidateQueries')
      setup(qc)
      const es = FakeEventSource.instances[0]
      act(() => {
        for (let i = 1; i <= 150; i++) es.emit(i, 'run.state_changed', undefined, '2020-01-01T00:00:00Z')
      })
      expect(spy).not.toHaveBeenCalled()
      act(() => {
        vi.advanceTimersByTime(400)
      })
      expect(spy).toHaveBeenCalledTimes(1)
    } finally {
      vi.useRealTimers()
    }
  })

  it('a later mount resumes from the stored seq', () => {
    const first = setup()
    act(() => FakeEventSource.instances[0].emit(7))
    first.unmount()
    expect(FakeEventSource.instances[0].closed).toBe(true)
    setup()
    expect(FakeEventSource.instances[1].url).toBe(`/api/v1/orgs/${ORG}/events/stream?after=7`)
  })

  it('an error only marks the stream as reconnecting and keeps the data', () => {
    const { result } = setup()
    const es = FakeEventSource.instances[0]
    act(() => {
      es.onopen?.()
      es.emit(1)
      es.onerror?.()
    })
    expect(result.current.status).toBe('reconnecting')
    expect(result.current.events).toHaveLength(1)
    act(() => es.onopen?.())
    expect(result.current.status).toBe('live')
  })

  it('invalidates queries by event type', () => {
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'invalidateQueries')
    setup(qc)
    act(() => FakeEventSource.instances[0].emit(2, 'connection_test.stage', 'op_1'))
    expect(spy).toHaveBeenCalledTimes(1)
  })

  it.each([
    // The orchestrator host is a policy key: a publish also refreshes the host list and the doctor shown.
    ['policy.published', ['policy/effective', 'projects/prj_1/policy/effective', 'catherd/hosts', 'catherd/status'], ['model-profiles']],
    ['catherd_secret.set', ['catherd/secrets', 'projects/prj_1/readiness', 'system/preflight', 'setup'], ['policy/draft', 'model-profiles']],
    ['model_profile_version.published', ['model-profiles/mpf_1', 'model-profile-versions/mpv_1', 'projects/prj_1/readiness'], ['policy/draft']],
    ['catherd.doctor_completed', ['catherd/status', 'health', 'projects/prj_1/readiness'], ['policy/draft']],
    ['system.preflight_completed', ['system/preflight', 'health', 'setup'], ['policy/draft']],
    ['setup.step_skipped', ['setup'], ['health', 'projects']],
  ])('%s refreshes the matching queries only', (type, stale, fresh) => {
    const qc = new QueryClient()
    const key = (p: string) => [`/api/v1/orgs/${ORG}/${p}`]
    for (const p of [...stale, ...fresh]) qc.setQueryData(key(p), {})
    setup(qc)
    act(() => FakeEventSource.instances[0].emit(1, type))
    for (const p of stale) expect(qc.getQueryState(key(p))?.isInvalidated, `${p} should refresh`).toBe(true)
    for (const p of fresh) expect(qc.getQueryState(key(p))?.isInvalidated, `${p} should stay`).toBe(false)
  })
})
