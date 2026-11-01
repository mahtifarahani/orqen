import { useMutation, useQueryClient } from '@tanstack/react-query'
import {
  getStatusQueryKey,
  status,
  useBootstrap,
  useLogout as useLogoutMutation,
  useRedeem,
  useStatus,
} from '../generated/auth/auth'
import { setCsrf } from '../mutator'
import { ok } from '../problem'

export const authStatusKey = getStatusQueryKey()

export function useAuthStatus() {
  return useStatus({
    query: {
      queryKey: authStatusKey,
      queryFn: async ({ signal }) => {
        const res = await status({ signal })
        setCsrf(ok(res).csrf_token ?? null)
        return res
      },
      select: (res) => ok(res),
      staleTime: 30_000,
      retry: false,
    },
  })
}

/** `bootstrap` claims a fresh install; `redeem` signs a new browser in with a login-link token. */
export function useClaimToken(kind: 'bootstrap' | 'redeem') {
  const qc = useQueryClient()
  const bootstrap = useBootstrap()
  const redeem = useRedeem()
  const m = kind === 'bootstrap' ? bootstrap : redeem
  return useMutation({
    mutationKey: ['claimToken', kind],
    mutationFn: async (token: string) => {
      const res = await m.mutateAsync({ data: { token } })
      setCsrf(ok(res).csrf_token)
      return ok(res)
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: authStatusKey }),
  })
}

export function useLogout() {
  const qc = useQueryClient()
  return useLogoutMutation({
    mutation: {
      onSuccess: () => {
        setCsrf(null)
        qc.clear()
      },
    },
  })
}
