import { useCallback, useState } from 'react'
import { newIdempotencyKey } from '@/api/idempotency'

/** One Idempotency-Key per user intent: keep it while a click is retried, renew it after the effect succeeded. */
export function useIntentKey() {
  const [key, setKey] = useState(newIdempotencyKey)
  const renew = useCallback(() => setKey(newIdempotencyKey()), [])
  return [key, renew] as const
}
