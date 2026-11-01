import { useTranslation } from 'react-i18next'
import type { EventDto, RunDto } from '@/api/generated/model'
import { useRunTimeline } from '@/api/hooks/tasks'
import { Ltr } from '@/components/ltr'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { Badge } from '@/components/ui/badge'
import { formatDateTime } from '@/lib/format'
import { useLocale } from '@/lib/use-locale'

function detail(t: ReturnType<typeof useTranslation>['t'], e: EventDto): string {
  const p = e.payload as Record<string, unknown>
  switch (e.type) {
    case 'run.state_changed':
      return `${t(`run.state.${String(p.from)}`, { defaultValue: String(p.from) })} → ${t(`run.state.${String(p.to)}`, { defaultValue: String(p.to) })}`
    case 'run.stage_completed':
      return t(`run.stage.${String(p.stage)}`, { defaultValue: String(p.stage) })
    case 'run.held':
      return t(`run.hold.${String(p.code)}`, { defaultValue: String(p.code) })
    case 'run.needs_input':
    case 'inbox.opened':
    case 'inbox.answered':
      return t(`inbox.reason.${String(p.reason)}`, { defaultValue: String(p.reason ?? '') })
    default:
      return ''
  }
}

export function TimelineTab({ orgId, run }: { orgId: string; run: RunDto | undefined }) {
  const { t } = useTranslation()
  const locale = useLocale()
  const timeline = useRunTimeline(orgId, run?.id)
  if (!run) return <p className="text-sm text-muted-foreground">{t('taskDetail.noRun')}</p>
  if (timeline.isPending) return <LoadingBlock />
  if (timeline.isError) return <LoadFailed title={t('taskDetail.timelineFailed')} error={timeline.error} onRetry={() => void timeline.refetch()} />
  if (timeline.data.length === 0) return <p className="text-sm text-muted-foreground">{t('taskDetail.timelineEmpty')}</p>
  return (
    <ol className="flex flex-col divide-y text-sm" aria-label={t('taskDetail.tabs.timeline')}>
      {timeline.data.map((e) => (
        <li key={e.seq} className="flex flex-wrap items-center gap-x-3 gap-y-1 py-2">
          <span className="text-xs text-muted-foreground tabular-nums">{formatDateTime(e.created_at, locale)}</span>
          <Badge variant="outline">
            <Ltr className="font-sans">{t(`run.event.${e.type.replaceAll('.', '_')}`, { defaultValue: e.type })}</Ltr>
          </Badge>
          <span>{detail(t, e)}</span>
          <span className="text-xs text-muted-foreground">{t(`run.actor.${e.actor}`, { defaultValue: e.actor })}</span>
        </li>
      ))}
    </ol>
  )
}
