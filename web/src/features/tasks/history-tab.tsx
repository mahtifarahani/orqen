import { useTranslation } from 'react-i18next'
import type { RunDto, TaskDto } from '@/api/generated/model'
import { Ltr } from '@/components/ltr'
import { RunStateBadge } from '@/components/run-state-badge'
import { Badge } from '@/components/ui/badge'
import { formatDateTime } from '@/lib/format'
import { useLocale } from '@/lib/use-locale'

export function HistoryTab({ task, run }: { orgId: string; task: TaskDto; run: RunDto | undefined }) {
  const { t } = useTranslation()
  const locale = useLocale()
  return (
    <div className="grid gap-6 lg:grid-cols-2">
      <section aria-labelledby="history-attempts" className="flex flex-col gap-2">
        <h2 id="history-attempts" className="text-lg font-semibold">
          {t('taskDetail.attempts')}
        </h2>
        {!run || run.attempts.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t('taskDetail.noRun')}</p>
        ) : (
          <ol className="flex flex-col divide-y text-sm">
            {run.attempts.map((a) => (
              <li key={a.number} className="flex flex-wrap items-baseline gap-x-3 gap-y-1 py-2">
                <strong className="tabular-nums">#{a.number}</strong>
                <Badge variant="outline">{t(`taskDetail.attemptReason.${a.reason}`)}</Badge>
                <span className="text-muted-foreground">{t(`run.stage.${a.from_stage}`, { defaultValue: a.from_stage })}</span>
                <span className="text-xs text-muted-foreground">{formatDateTime(a.started_at, locale)}</span>
                {a.ended_at && <span className="text-xs text-muted-foreground">{a.outcome}</span>}
              </li>
            ))}
          </ol>
        )}
      </section>
      <section aria-labelledby="history-runs" className="flex flex-col gap-2">
        <h2 id="history-runs" className="text-lg font-semibold">
          {t('taskDetail.runs')}
        </h2>
        {task.runs.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t('taskDetail.noRun')}</p>
        ) : (
          <ol className="flex flex-col divide-y text-sm">
            {task.runs.map((r) => (
              <li key={r.id} className="flex flex-wrap items-center gap-x-3 gap-y-1 py-2">
                <Ltr className="text-xs">{r.id.slice(-8)}</Ltr>
                <RunStateBadge state={r.state} />
                <span className="text-xs text-muted-foreground">{formatDateTime(r.updated_at, locale)}</span>
                {run && r.id === run.id && run.supersedes && <Badge variant="secondary">{t('taskDetail.supersedes')}</Badge>}
                {run && r.id === run.superseded_by && <Badge variant="secondary">{t('taskDetail.supersededBy')}</Badge>}
              </li>
            ))}
          </ol>
        )}
        {run?.supersedes && (
          <p className="text-xs text-muted-foreground">
            {t('taskDetail.supersedes')}: <Ltr>{run.supersedes.slice(-8)}</Ltr>
          </p>
        )}
        {run?.superseded_by && (
          <p className="text-xs text-muted-foreground">
            {t('taskDetail.supersededBy')}: <Ltr>{run.superseded_by.slice(-8)}</Ltr>
          </p>
        )}
      </section>
    </div>
  )
}
