import { useTranslation } from 'react-i18next'
import { Link, useParams } from 'react-router'
import { useCatherdStatus } from '@/api/hooks/catherd'
import { useConnections } from '@/api/hooks/connections'
import { useProjectCounts } from '@/api/hooks/projects'
import { useEventFeed } from '@/components/events-context'
import { Ltr } from '@/components/ltr'
import { StatusBadge } from '@/components/status-badge'
import { PendingSetupCard } from './pending-setup-card'
import { AutomationCard } from './automation-card'
import { CodingSlotCard, MergeRequestsCard, PublishingCard } from './publish-cards'
import { ActiveRunsCard, InboxCard, IntakeCard } from './work-cards'
import { ProjectStateBadge } from '@/features/projects/project-state-badge'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { LoadFailed } from '@/components/query-state'
import { Skeleton } from '@/components/ui/skeleton'
import { formatNumber, formatTime } from '@/lib/format'
import { useLocale } from '@/lib/use-locale'

const STATUSES = ['untested', 'reachable', 'verified', 'degraded', 'unreachable'] as const

export function OverviewPage() {
  const { t } = useTranslation()
  const locale = useLocale()
  const { orgId = '' } = useParams()
  const connections = useConnections(orgId)
  const projects = useProjectCounts(orgId)
  const catherd = useCatherdStatus(orgId)
  const feed = useEventFeed()
  const byState = (s: string) => projects.data?.items.filter((p) => p.state === s).length ?? 0
  const latest = feed.events.slice(-50).reverse()
  const count = (s: string) => connections.data?.filter((c) => c.status === s).length ?? 0
  const ready = connections.data?.filter((c) => c.readiness.unattended_ready).length ?? 0

  return (
    <div className="flex flex-col gap-6">
      <h1 className="text-2xl font-semibold">{t('overview.title')}</h1>
      <PendingSetupCard />
      <div className="grid gap-4 md:grid-cols-2">
        <AutomationCard orgId={orgId} />
        <ActiveRunsCard orgId={orgId} />
        <InboxCard orgId={orgId} />
        <IntakeCard orgId={orgId} />
        <MergeRequestsCard orgId={orgId} />
        <PublishingCard orgId={orgId} />
        <CodingSlotCard orgId={orgId} />
        <Card>
          <CardHeader>
            <CardTitle>{t('overviewPage.connections')}</CardTitle>
          </CardHeader>
          <CardContent>
            {connections.isPending ? (
              <Skeleton className="h-24 w-full" />
            ) : (
              <>
                <p className="mb-3 text-sm text-muted-foreground">
                  {t('overviewPage.total')}: {formatNumber(connections.data?.length ?? 0, locale)}
                </p>
                <ul className="flex flex-col gap-2" aria-label={t('overviewPage.byStatus')}>
                  {STATUSES.map((s) => (
                    <li key={s} className="flex items-center justify-between">
                      <StatusBadge status={s} />
                      <span className="tabular-nums">{formatNumber(count(s), locale)}</span>
                    </li>
                  ))}
                </ul>
              </>
            )}
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle>{t('overviewPage.automation')}</CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-sm">{t('overviewPage.automationBody', { count: formatNumber(ready, locale) })}</p>
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle>{t('overviewPage.projects')}</CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            {projects.isPending && <Skeleton className="h-16 w-full" role="status" aria-label={t('app.loading')} />}
            {projects.isError && <LoadFailed title={t('projects.loadFailed')} error={projects.error} onRetry={() => void projects.refetch()} />}
            {projects.data && projects.data.items.length === 0 && (
              <>
                <p className="text-sm text-muted-foreground">{t('overviewPage.projectsEmpty')}</p>
                <Button asChild size="sm" className="self-start">
                  <Link to={`/orgs/${orgId}/projects/discover`}>{t('projects.discover')}</Link>
                </Button>
              </>
            )}
            {projects.data && projects.data.items.length > 0 && (
              <>
                <p className="text-sm text-muted-foreground">
                  {t('overviewPage.total')}: {formatNumber(projects.data.items.length, locale)}
                </p>
                <ul className="flex flex-col gap-2" aria-label={t('overviewPage.projects')}>
                  {(['ready', 'enabled', 'enabled_blocked', 'draft'] as const).map((s) => (
                    <li key={s} className="flex items-center justify-between">
                      <ProjectStateBadge state={s} />
                      <span className="tabular-nums">{formatNumber(byState(s), locale)}</span>
                    </li>
                  ))}
                </ul>
                <Button asChild variant="link" className="self-start px-0">
                  <Link to={`/orgs/${orgId}/projects`}>{t('overviewPage.allProjects')}</Link>
                </Button>
              </>
            )}
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle>{t('overviewPage.catherd')}</CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-3 text-sm">
            {catherd.isPending && <Skeleton className="h-16 w-full" role="status" aria-label={t('app.loading')} />}
            {catherd.isError && <LoadFailed title={t('catherd.loadFailed')} error={catherd.error} onRetry={() => void catherd.refetch()} />}
            {catherd.data && (
              <>
                <p className="flex flex-wrap items-center gap-2">
                  {catherd.data.available ? (
                    <>
                      <Ltr>{catherd.data.version}</Ltr>
                      <Badge variant={catherd.data.supported ? 'default' : 'destructive'}>{catherd.data.supported ? t('catherd.supported') : t('catherd.notSupported')}</Badge>
                    </>
                  ) : (
                    <Badge variant="destructive">{t('catherd.unavailable')}</Badge>
                  )}
                </p>
                <p className="text-muted-foreground">
                  {catherd.data.last_doctor
                    ? catherd.data.last_doctor.ready
                      ? t('catherd.doctor.ready')
                      : t('catherd.doctor.notReady')
                    : t('catherd.doctor.never')}
                </p>
                <Button asChild variant="link" className="self-start px-0">
                  <Link to={`/orgs/${orgId}/health`}>{t('overviewPage.openHealth')}</Link>
                </Button>
              </>
            )}
          </CardContent>
        </Card>
      </div>
      <Card>
        <CardHeader>
          <CardTitle>{t('overviewPage.feed')}</CardTitle>
        </CardHeader>
        <CardContent>
          {latest.length === 0 ? (
            <p className="text-sm text-muted-foreground">{t('overviewPage.feedEmpty')}</p>
          ) : (
            <ol className="flex flex-col divide-y text-sm" aria-label={t('overviewPage.feedLabel')}>
              {latest.map((e) => (
                <li key={e.seq} className="flex flex-wrap items-baseline gap-x-3 gap-y-1 py-2">
                  <time dateTime={e.created_at} className="text-xs text-muted-foreground tabular-nums">
                    {formatTime(e.created_at, locale)}
                  </time>
                  <Ltr>{e.type}</Ltr>
                  <Ltr className="text-xs text-muted-foreground">{e.subject.id}</Ltr>
                </li>
              ))}
            </ol>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
