import { useState } from 'react'
import type { OperationDto } from '@/api/generated/model'
import { newIdempotencyKey } from '@/api/idempotency'
import { useOperation } from '@/api/hooks/connections'

/**
 * Runs a long operation: one Idempotency-Key per click intent (kept on failure so a retry replays, renewed
 * after the operation was accepted), then tracks the operation until it finishes.
 *
 *   const runner = useOperationRunner(orgId)
 *   const start = useSomeOperationMutation(runner.key)
 *   start.mutate(vars, { onSuccess: runner.started })
 */
export function useOperationRunner(orgId: string) {
  const [key, setKey] = useState(newIdempotencyKey)
  const [opId, setOpId] = useState<string | null>(null)
  const operation = useOperation(orgId, opId)
  const op: OperationDto | undefined = operation.data
  const active = op?.status === 'pending' || op?.status === 'running'
  return {
    key,
    opId,
    operation: op,
    running: !!opId && (!op || active),
    failed: op?.status === 'failed',
    succeeded: op?.status === 'succeeded',
    /** Pass as the mutation's `onSuccess`. */
    started: (res: { data: unknown }) => {
      setOpId((res.data as OperationDto).id)
      setKey(newIdempotencyKey())
    },
    reset: () => setOpId(null),
  }
}
