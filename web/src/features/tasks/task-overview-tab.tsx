import { useTranslation } from 'react-i18next'
import { Link } from 'react-router'
import type { RunDto, TaskDto } from '@/api/generated/model'
import { Ltr } from '@/components/ltr'
import { RiskBadge } from '@/components/run-state-badge'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { formatDateTime } from '@/lib/format'
import { useLocale } from '@/lib/use-locale'

export function OverviewTab({ orgId, task, run }: { orgId: string; task: TaskDto; run: RunDto | undefined }) {
  const { t } = useTranslation()
  const locale = useLocale()
  const snap = task.snapshot
  if (!snap) return <p className="text-sm text-muted-foreground">{t('taskDetail.noSnapshot')}</p>
  return (
    <div className="grid gap-4 lg:grid-cols-2">
      <Card>
        <CardHeader>
          <CardTitle>{t('taskDetail.acceptance')}</CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col gap-2 text-sm">
          <p className="text-muted-foreground">
            {t('taskDetail.acceptanceSource')}: {t(`taskDetail.acceptanceSources.${snap.acceptance.source}`)}
          </p>
          {snap.acceptance.items.length > 0 ? (
            <ul className="list-disc ps-5" dir="auto">
              {snap.acceptance.items.map((item, i) => (
                <li key={i} dir="auto">
                  {item}
                </li>
              ))}
            </ul>
          ) : (
            <p dir="auto" className="whitespace-pre-wrap">
              {snap.acceptance.text || t('taskDetail.noAcceptance')}
            </p>
          )}
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle>{t('taskDetail.triage')}</CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col gap-2 text-sm">
          {run?.triage ? (
            <>
              <p>
                {t('taskDetail.outcome')}: <strong>{t(`triage.outcome.${run.triage.outcome}`, { defaultValue: run.triage.outcome })}</strong>
              </p>
              <p className="flex items-center gap-2">
                {t('taskDetail.risk')}: <RiskBadge risk={run.triage.risk} />
              </p>
              <p>
                {t('taskDetail.decisionSource')}: {t(`triage.source.${run.triage.source}`, { defaultValue: run.triage.source })}
              </p>
              {run.triage.risk_reasons.length > 0 && (
                <p className="text-muted-foreground">
                  {t('taskDetail.riskReasons')}: {run.triage.risk_reasons.map((r) => t(`triage.riskReason.${r.split(':')[1] ?? r}`, { defaultValue: r })).join(' · ')}
                </p>
              )}
              <p className="text-xs text-muted-foreground">{formatDateTime(run.triage.decided_at, locale)}</p>
            </>
          ) : (
            <p className="text-muted-foreground">{t('taskDetail.noTriage')}</p>
          )}
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle>{t('taskDetail.description')}</CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col gap-3 text-sm">
          <p dir="auto" className="max-h-72 overflow-auto whitespace-pre-wrap">
            {snap.description || t('taskDetail.noDescription')}
          </p>
          {snap.description_truncated && <Badge variant="outline">{t('taskDetail.descriptionTruncated')}</Badge>}
          {snap.parent && (
            <p className="text-muted-foreground">
              {t('taskDetail.parent')}: <Ltr>{snap.parent.key}</Ltr> <span dir="auto">{snap.parent.summary}</span>
            </p>
          )}
          {snap.links.length > 0 && (
            <ul className="text-muted-foreground">
              {snap.links.map((l) => (
                <li key={`${l.type}${l.direction}${l.id}`}>
                  {l.type} · <Ltr>{l.key}</Ltr> <span dir="auto">{l.summary}</span>
                </li>
              ))}
            </ul>
          )}
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle>{t('taskDetail.sources')}</CardTitle>
        </CardHeader>
        <CardContent>
          {run && run.sources.length > 0 ? (
            <ul className="flex flex-col gap-2 text-sm" aria-label={t('taskDetail.sources')}>
              {run.sources.map((s) => (
                <li key={s.id} className="flex flex-wrap items-baseline gap-2">
                  <Badge variant="outline">{t(`taskDetail.sourceKind.${s.kind}`, { defaultValue: s.kind })}</Badge>
                  <span dir="auto">{s.label}</span>
                  <span className="text-xs text-muted-foreground">
                    {t('taskDetail.revisionAdded', { n: s.revision_added })}
                  </span>
                  {s.carried && <Badge variant="secondary">{t('taskDetail.carried')}</Badge>}
                  {s.text && (
                    <span className="basis-full text-muted-foreground" dir="auto">
                      {s.text}
                    </span>
                  )}
                </li>
              ))}
            </ul>
          ) : (
            <p className="text-sm text-muted-foreground">{t('taskDetail.noSources')}</p>
          )}
          {run?.open_inbox_id && (
            <Link to={`/orgs/${orgId}/inbox?item=${run.open_inbox_id}`} className="mt-3 inline-block text-sm underline underline-offset-4">
              {t('run.openQuestion')}
            </Link>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
