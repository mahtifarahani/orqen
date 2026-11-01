import { useGetOrgHealth } from '../generated/health/health'
import { ok } from '../problem'

export function useOrgHealth(orgId: string) {
  return useGetOrgHealth(orgId, { query: { select: (res) => ok(res), refetchInterval: 30_000 } })
}
