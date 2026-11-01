import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import { useEventFeed } from './events-context'

export function LiveIndicator() {
  const { t } = useTranslation()
  const { status } = useEventFeed()
  const color =
    status === 'live' ? 'bg-emerald-500' : status === 'reconnecting' ? 'bg-amber-500' : 'bg-muted-foreground'
  return (
    <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground" role="status" data-status={status}>
      <span className={cn('size-2 rounded-full', color)} aria-hidden />
      {t(`live.${status}`)}
    </span>
  )
}
