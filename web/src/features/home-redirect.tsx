import { useTranslation } from 'react-i18next'
import { Navigate } from 'react-router'
import { useOrgs } from '@/api/hooks/orgs'
import { useSetup } from '@/api/hooks/setup'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Skeleton } from '@/components/ui/skeleton'
import { getLastOrg } from '@/lib/last-org'
import { problemMessage } from '@/lib/problem-message'

/** Where `/` goes: the wizard until it is finished or hidden, otherwise the Overview. */
function homeTarget(orgId: string, wizard: { dismissed: boolean; completed_at?: string | null } | undefined): string {
  if (wizard && !wizard.dismissed && !wizard.completed_at) return `/orgs/${orgId}/setup`
  return `/orgs/${orgId}/overview`
}

/** `/` → first-run setup (no organization yet, or an unfinished wizard), else the last used org's overview. */
export function HomeRedirect() {
  const { t } = useTranslation()
  const orgs = useOrgs()
  const target = orgs.data ? (orgs.data.find((o) => o.id === getLastOrg()) ?? orgs.data[0]) : undefined
  const setup = useSetup(target?.id)
  if (orgs.isPending) return <Skeleton className="m-6 h-8 w-48" />
  if (orgs.isError) return <Alert variant="destructive" className="m-6 w-auto"><AlertDescription>{problemMessage(t, orgs.error)}</AlertDescription></Alert>
  if (!target) return <Navigate to="/setup/new" replace />
  if (setup.isPending) return <Skeleton className="m-6 h-8 w-48" />
  // A wizard that cannot be read must not lock the user out of their organization.
  return <Navigate to={homeTarget(target.id, setup.data)} replace />
}
