/**
 * Rule: create the key ONCE per user intent (dialog open / first click) and reuse it on retry,
 * so a retried request replays instead of creating a second effect.
 */
export const newIdempotencyKey = () => crypto.randomUUID()
