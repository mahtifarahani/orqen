import { useTranslation } from 'react-i18next'
import { Link } from 'react-router'
import { useExternalWrites } from '@/api/hooks/externalWrites'
import { useTaskSummary } from '@/api/hooks/tasks'
import { Ltr } from '@/components/ltr'
import { LoadingBlock } from '@/components/query-state'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { formatNumber } from '@/lib/format'
import { useLocale } from '@/lib/use-locale'

export function MergeRequestsCard({ orgId }: { orgId: string }) {
  const { t } = useTranslation()
  const locale = useLocale()
  const summary = useTaskSummary(orgId)
  const mr = summary.data?.merge_requests
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('overviewPage.mergeRequests')}</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-3 text-sm">
        {summary.isPending && <LoadingBlock rows={2} />}
        {mr && (
          <ul className="flex flex-col gap-2" aria-label={t('overviewPage.mergeRequests')}>
            <li className="flex items-center justify-between">
              <span>{t('overviewPage.mrReady')}</span>
              <span className="tabular-nums">{formatNumber(mr.ready, locale)}</span>
            </li>
            <li className="flex items-center justify-between">
              <span>{t('overviewPage.mrAwaitingCi')}</span>
              <span className="tabular-nums">{formatNumber(mr.awaiting_ci, locale)}</span>
            </li>
            <li className="flex items-center justify-between">
              <span>{t('overviewPage.mrDryRun')}</span>
              <span className="tabular-nums">{formatNumber(mr.dry_run, locale)}</span>
            </li>
          </ul>
        )}
        <Button asChild variant="link" className="self-start px-0">
          <Link to={`/orgs/${orgId}/merge-requests`}>{t('overviewPage.allMergeRequests')}</Link>
        </Button>
      </CardContent>
    </Card>
  )
}

export function PublishingCard({ orgId }: { orgId: string }) {
  const { t } = useTranslation()
  const locale = useLocale()
  const settings = useExternalWrites(orgId)
  const summary = useTaskSummary(orgId)
  const ops = summary.data?.external_operations
  const state = settings.data ? (settings.data.paused ? 'paused' : settings.data.mode === 'live' ? 'live' : 'dry_run') : null
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('overviewPage.publishing')}</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-3 text-sm">
        {state && (
          <div className="flex items-center gap-2">
            <Badge variant="outline" data-write-mode={state}>
              {t(`publishing.badge.${state}`)}
            </Badge>
            <span className="text-muted-foreground">{t(`publishing.badge.${state}Hint`)}</span>
          </div>
        )}
        {ops && (
          <ul className="flex flex-col gap-2" aria-label={t('overviewPage.publishing')}>
            <li className="flex items-center justify-between">
              <span>{t('overviewPage.opsAwaiting')}</span>
              <span className="tabular-nums">{formatNumber(ops.awaiting_approval, locale)}</span>
            </li>
            <li className="flex items-center justify-between">
              <span>{t('overviewPage.opsUnknown')}</span>
              <span className="tabular-nums">{formatNumber(ops.outcome_unknown, locale)}</span>
            </li>
          </ul>
        )}
        <Button asChild variant="link" className="self-start px-0">
          <Link to={`/orgs/${orgId}/settings?tab=publishing`}>{t('overviewPage.publishingSettings')}</Link>
        </Button>
      </CardContent>
    </Card>
  )
}

export function CodingSlotCard({ orgId }: { orgId: string }) {
  const { t } = useTranslation()
  const locale = useLocale()
  const summary = useTaskSummary(orgId)
  const slot = summary.data?.coding_slot
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('overviewPage.codingSlot')}</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-2 text-sm">
        {summary.isPending && <LoadingBlock rows={1} />}
        {slot && (
          <>
            <p data-slot-busy={!!slot.run_id}>{slot.run_id ? <>{t('overviewPage.slotBusy')} <Ltr className="font-mono text-xs">{slot.run_id}</Ltr></> : t('overviewPage.slotFree')}</p>
            <p className="text-muted-foreground">{t('overviewPage.slotQueued', { count: formatNumber(slot.queued, locale) })}</p>
          </>
        )}
      </CardContent>
    </Card>
  )
}
