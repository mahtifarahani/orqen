import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createInvalidationBatcher, type EventDto } from '@/api/sse'

const ev = (seq: number, type: string, operation_id?: string) => ({ seq, type, operation_id }) as unknown as EventDto

describe('createInvalidationBatcher', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  it('applies one invalidation per distinct event kind for a burst', () => {
    const apply = vi.fn()
    const b = createInvalidationBatcher(apply, 100)
    for (let i = 0; i < 200; i++) b.add(ev(i, i % 2 ? 'run.state_changed' : 'inbox.opened'))
    expect(apply).toHaveBeenCalledTimes(1) // the first event applies at once
    vi.advanceTimersByTime(100)
    expect(apply).toHaveBeenCalledTimes(3) // then each kind of the rest of the burst, once
    vi.advanceTimersByTime(500)
    expect(apply).toHaveBeenCalledTimes(3)
  })

  it('keeps operation events of different operations apart and can be cancelled', () => {
    const apply = vi.fn()
    const b = createInvalidationBatcher(apply, 100)
    b.add(ev(1, 'operation.progress', 'op_1'))
    b.add(ev(2, 'operation.progress', 'op_2'))
    b.add(ev(3, 'operation.progress', 'op_3'))
    vi.advanceTimersByTime(100)
    expect(apply).toHaveBeenCalledTimes(3)
    vi.advanceTimersByTime(500)
    b.add(ev(4, 'run.created'))
    b.add(ev(5, 'run.updated'))
    b.cancel()
    vi.advanceTimersByTime(500)
    expect(apply).toHaveBeenCalledTimes(4)
  })
})
