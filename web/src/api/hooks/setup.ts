import { useQueryClient } from '@tanstack/react-query'
import {
  getGetSetupQueryKey,
  useCompleteSetupStep as useCompleteMutation,
  useFinishSetup as useFinishMutation,
  useGetSetup,
  usePatchSetup as usePatchMutation,
  useReopenSetupStep as useReopenMutation,
  useSkipSetupStep as useSkipMutation,
} from '../generated/setup/setup'
import { ifMatch } from '../mutator'
import { ok } from '../problem'

export function useSetup(orgId: string | undefined) {
  return useGetSetup(orgId ?? '', { query: { enabled: !!orgId, select: (res) => ok(res) } })
}

/**
 * The wizard document is replaced as a whole under If-Match. Every mutation answers with the new document,
 * which is written straight into the cache so the next call already carries the new revision.
 */
export function useSetupActions(orgId: string, revision: number) {
  const qc = useQueryClient()
  const request = { headers: { 'If-Match': ifMatch(revision) } }
  const mutation = { onSuccess: (res: unknown) => qc.setQueryData(getGetSetupQueryKey(orgId), res) }
  return {
    patch: usePatchMutation({ request, mutation }),
    complete: useCompleteMutation({ request, mutation }),
    skip: useSkipMutation({ request, mutation }),
    reopen: useReopenMutation({ request, mutation }),
    finish: useFinishMutation({ request, mutation }),
  }
}
