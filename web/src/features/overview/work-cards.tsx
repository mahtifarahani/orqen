import { useTranslation } from 'react-i18next'
import { Link } from 'react-router'
import { inboxRows, useInbox } from '@/api/hooks/inbox'
import { useTaskSummary } from '@/api/hooks/tasks'
import { projectRows, useProjects } from '@/api/hooks/projects'
import { Ltr } from '@/components/ltr'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { formatDateTime, formatNumber } from '@/lib/format'
import { useLocale } from '@/lib/use-locale'

const GROUPS: { key: string; states: string[] }[] = [
  { key: 'inProgress', states: ['discovered', 'gathering_context', 'triaging', 'planned', 'coding', 'verifying', 'publishing_draft', 'reviewing', 'fixing', 'awaiting_ci'] },
  { key: 'waitingOnYou', states: ['needs_input'] },
  { key: 'waitingOther', states: ['waiting_network', 'waiting_dependency'] },
  { key: 'queued', states: ['queued', 'ready_for_merge'] },
]

export function ActiveRunsCard({ orgId }: { orgId: string }) {
  const { t } = useTranslation()
  const locale = useLocale()
  const summary = useTaskSummary(orgId)
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('overviewPage.activeRuns')}</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-3 text-sm">
        {summary.isPending && <LoadingBlock rows={2} />}
        {summary.isError && <LoadFailed title={t('tasks.loadFailed')} error={summary.error} onRetry={() => void summary.refetch()} />}
        {summary.data && (
          <>
            <ul className="flex flex-col gap-2" aria-label={t('overviewPage.activeRuns')}>
              {GROUPS.map((g) => (
                <li key={g.key} className="flex items-center justify-between">
                  <span>{t(`overviewPage.groups.${g.key}`)}</span>
                  <span className="tabular-nums">{formatNumber(g.states.reduce((n, s) => n + (summary.data.runs_by_state[s] ?? 0), 0), locale)}</span>
                </li>
              ))}
            </ul>
            {summary.data.security && (summary.data.security.open_blocking > 0 || summary.data.security.scans_running > 0) && (
              <p className="flex flex-wrap items-center gap-2" data-testid="overview-security">
                <span className="font-medium">{t('overviewPage.securityCard')}</span>
                {summary.data.security.open_blocking > 0 && <Badge variant="destructive">{t('overviewPage.securityBlocking')}: {formatNumber(summary.data.security.open_blocking, locale)}</Badge>}
                {summary.data.security.scans_running > 0 && <Badge variant="secondary">{t('overviewPage.securityRunning')}: {formatNumber(summary.data.security.scans_running, locale)}</Badge>}
              </p>
            )}
            {summary.data.candidates > 0 && <p className="text-muted-foreground">{t('overviewPage.candidates', { count: formatNumber(summary.data.candidates, locale) })}</p>}
            <Button asChild variant="link" className="self-start px-0">
              <Link to={`/orgs/${orgId}/tasks`}>{t('overviewPage.allTasks')}</Link>
            </Button>
          </>
        )}
      </CardContent>
    </Card>
  )
}

export function InboxCard({ orgId }: { orgId: string }) {
  const { t } = useTranslation()
  const locale = useLocale()
  const inbox = useInbox(orgId, { status: 'open' })
  const rows = inboxRows(inbox.data)
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('overviewPage.inboxCard')}</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-3 text-sm">
        {inbox.isPending && <LoadingBlock rows={2} />}
        {inbox.isError && <LoadFailed title={t('inbox.loadFailed')} error={inbox.error} onRetry={() => void inbox.refetch()} />}
        {inbox.isSuccess && (
          <>
            <p className="text-muted-foreground">
              {rows.length === 0 ? t('inbox.empty') : t('overviewPage.inboxOpen', { count: formatNumber(rows.length, locale) })}
            </p>
            <ul className="flex flex-col gap-2">
              {rows.slice(0, 3).map((it) => (
                <li key={it.id} className="flex flex-wrap items-center gap-2">
                  <Link to={`/orgs/${orgId}/inbox?item=${it.id}`} className="underline underline-offset-4">
                    <Ltr>{it.issue_key}</Ltr>
                  </Link>
                  <Badge variant="outline">{t(`inbox.reason.${it.reason}`, { defaultValue: it.reason })}</Badge>
                  <span className="text-xs text-muted-foreground">{formatDateTime(it.created_at, locale)}</span>
                </li>
              ))}
            </ul>
            <Button asChild variant="link" className="self-start px-0">
              <Link to={`/orgs/${orgId}/inbox`}>{t('overviewPage.openInbox')}</Link>
            </Button>
          </>
        )}
      </CardContent>
    </Card>
  )
}

export function IntakeCard({ orgId }: { orgId: string }) {
  const { t } = useTranslation()
  const locale = useLocale()
  const summary = useTaskSummary(orgId)
  const projects = projectRows(useProjects(orgId, '').data)
  const nameOf = (id: string) => projects.find((p) => p.id === id)?.name ?? id
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('overviewPage.intakeCard')}</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-2 text-sm">
        {summary.isPending && <LoadingBlock rows={2} />}
        {summary.isError && <LoadFailed title={t('intake.loadFailed')} error={summary.error} onRetry={() => void summary.refetch()} />}
        {summary.data && summary.data.intake.length === 0 && <p className="text-muted-foreground">{t('overviewPage.intakeEmpty')}</p>}
        {summary.data && (
          <ul className="flex flex-col gap-3" aria-label={t('overviewPage.intakeCard')}>
            {summary.data.intake.map((i) => (
              <li key={i.project_id} className="flex flex-col gap-1">
                <Link to={`/orgs/${orgId}/projects/${i.project_id}?tab=intake`} className="font-medium underline-offset-4 hover:underline">
                  {nameOf(i.project_id)}
                </Link>
                <span className="text-muted-foreground">
                  {t('intake.lastPoll')}: {i.last_success_at ? formatDateTime(i.last_success_at, locale) : t('common.never')}
                </span>
                {i.blocked && <Badge variant="destructive" className="self-start">{t('intake.blocked.title')}</Badge>}
                {i.waiting_network && <Badge variant="secondary" className="self-start">{t('intake.waitingNetwork')}</Badge>}
                {i.last_error && !i.blocked && <Badge variant="destructive" className="self-start">{t(`intake.error.${i.last_error.code}`, { defaultValue: i.last_error.code })}</Badge>}
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  )
}
