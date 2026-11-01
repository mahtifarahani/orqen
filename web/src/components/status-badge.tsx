import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'

const TONES: Record<string, string> = {
  verified: 'bg-emerald-200 text-emerald-950 dark:bg-emerald-900 dark:text-emerald-100',
  reachable: 'bg-emerald-100 text-emerald-900 dark:bg-emerald-950 dark:text-emerald-200',
  degraded: 'bg-amber-100 text-amber-900 dark:bg-amber-950 dark:text-amber-200',
  unreachable: 'bg-red-100 text-red-900 dark:bg-red-950 dark:text-red-200',
  untested: 'bg-muted text-muted-foreground',
}

/** Connection status. Never "healthy": reachability only. */
export function StatusBadge({ status }: { status: string }) {
  const { t } = useTranslation()
  return (
    <Badge variant="outline" className={cn('border-transparent', TONES[status] ?? TONES.untested)} data-status={status}>
      {t(`connStatus.${status}`, { defaultValue: status })}
    </Badge>
  )
}
