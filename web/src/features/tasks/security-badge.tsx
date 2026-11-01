import { useTranslation } from 'react-i18next'
import type { TaskSecurityDto } from '@/api/generated/model'
import { Ltr } from '@/components/ltr'
import { Badge } from '@/components/ui/badge'

/**
 * The newest security scan of a task. Only a complete scan with no open blocking finding is green; a scan of an older
 * commit than the reviewed one carries a stale chip. Nothing is shown for a task that was never scanned.
 */
export function SecurityBadge({ security }: { security: TaskSecurityDto | null | undefined }) {
  const { t } = useTranslation()
  if (!security) return null
  const active = security.state === 'queued' || security.state === 'running'
  const clean = security.state === 'finished' && security.completion === 'complete' && security.open_blocking === 0
  let label: string
  let variant: 'default' | 'secondary' | 'destructive' | 'outline' = 'secondary'
  if (active) label = t(`tasks.security.${security.state}`)
  else if (security.open_blocking > 0) {
    label = t('tasks.security.blocking', { count: security.open_blocking })
    variant = 'destructive'
  } else if (clean) {
    label = t('tasks.security.clean')
    variant = 'default'
  } else label = t('tasks.security.incomplete')
  return (
    <span className="inline-flex flex-wrap items-center gap-1" data-security={clean ? 'clean' : active ? security.state : 'attention'}>
      <Badge variant={variant} aria-label={t('tasks.security.label', { sha: security.head_sha.slice(0, 12) })}>{label}</Badge>
      <Ltr className="text-xs text-muted-foreground">{security.head_sha.slice(0, 8)}</Ltr>
      {security.stale && <Badge variant="outline">{t('tasks.security.stale')}</Badge>}
    </span>
  )
}
