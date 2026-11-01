import { useTranslation } from 'react-i18next'
import { Link, useParams } from 'react-router'
import { useExternalWrites } from '@/api/hooks/externalWrites'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'

/** The write mode is always visible: DRY-RUN (simulated), LIVE, or PAUSED. It links to the Publishing settings. */
export function WriteModeBadge() {
  const { t } = useTranslation()
  const { orgId = '' } = useParams()
  const settings = useExternalWrites(orgId)
  if (!settings.data) return null
  const s = settings.data
  const state = s.paused ? 'paused' : s.mode === 'live' ? 'live' : 'dry_run'
  const tone =
    state === 'live'
      ? 'bg-red-100 text-red-900 dark:bg-red-950 dark:text-red-200'
      : state === 'paused'
        ? 'bg-amber-100 text-amber-900 dark:bg-amber-950 dark:text-amber-200'
        : 'bg-muted text-muted-foreground'
  return (
    <Link to={`/orgs/${orgId}/settings?tab=publishing`} className="rounded-md outline-none focus-visible:ring-2 focus-visible:ring-ring" data-testid="write-mode">
      <Badge variant="outline" className={cn('border-transparent', tone)} data-write-mode={state} title={t(`publishing.badge.${state}Hint`)}>
        {t(`publishing.badge.${state}`)}
      </Badge>
    </Link>
  )
}
