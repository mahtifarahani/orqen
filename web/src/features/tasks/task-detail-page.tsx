import { useTranslation } from 'react-i18next'
import { Link, useParams, useSearchParams } from 'react-router'
import { ArrowLeftIcon, ExternalLinkIcon, InboxIcon } from 'lucide-react'
import type { TaskDto } from '@/api/generated/model'
import { useRun, useTask } from '@/api/hooks/tasks'
import { Ltr } from '@/components/ltr'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { RiskBadge, RunBadges } from '@/components/run-state-badge'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { readinessText } from '@/lib/readiness-text'
import { DiffTab } from './diff-tab'
import { MergeRequestTab } from './merge-request-tab'
import { PublishingTab } from './publishing-tab'
import { ReviewTab } from './review-tab'
import { RolesTab } from './roles-tab'
import { SecurityTab } from './security-tab'
import { VerificationTab } from './verification-tab'
import { ContextTab } from './context-tab'
import { HistoryTab } from './history-tab'
import { OverviewTab } from './task-overview-tab'
import { PlanTab } from './plan-tab'
import { RunActions } from './run-actions'
import { TimelineTab } from './timeline-tab'
import { WorkspaceTab } from './workspace-tab'

export const TASK_TABS = ['overview', 'plan', 'timeline', 'context', 'workspace', 'roles', 'verification', 'diff', 'review', 'mergeRequest', 'security', 'publishing', 'history'] as const
type TaskTab = (typeof TASK_TABS)[number]
const isTab = (v: string | null): v is TaskTab => !!v && (TASK_TABS as readonly string[]).includes(v)

export function TaskDetailPage() {
  const { t } = useTranslation()
  const { orgId = '', taskId = '' } = useParams()
  const task = useTask(orgId, taskId)
  const [search, setSearch] = useSearchParams()
  const urlTab = search.get('tab')
  const tab: TaskTab = isTab(urlTab) ? urlTab : 'overview'
  const run = useRun(orgId, task.data?.run?.id)

  return (
    <div className="flex flex-col gap-6">
      <Button asChild variant="ghost" className="self-start">
        <Link to={`/orgs/${orgId}/tasks`}>
          <ArrowLeftIcon className="rtl:-scale-x-100" />
          {t('taskDetail.back')}
        </Link>
      </Button>
      {task.isPending && <LoadingBlock rows={2} />}
      {task.isError && <LoadFailed title={t('taskDetail.loadFailed')} error={task.error} onRetry={() => void task.refetch()} />}
      {task.data && (
        <>
          <Header orgId={orgId} task={task.data} run={run.data} />
          <Tabs value={tab} onValueChange={(v) => isTab(v) && setSearch({ tab: v }, { replace: true })}>
            <div className="overflow-x-auto">
              <TabsList aria-label={t('taskDetail.tabsLabel')}>
                {TASK_TABS.map((k) => (
                  <TabsTrigger key={k} value={k}>
                    {t(`taskDetail.tabs.${k}`)}
                  </TabsTrigger>
                ))}
              </TabsList>
            </div>
            <TabsContent value="overview" className="pt-3">
              <OverviewTab orgId={orgId} task={task.data} run={run.data} />
            </TabsContent>
            <TabsContent value="plan" className="pt-3">
              <PlanTab orgId={orgId} run={run.data} />
            </TabsContent>
            <TabsContent value="timeline" className="pt-3">
              <TimelineTab orgId={orgId} run={run.data} />
            </TabsContent>
            <TabsContent value="context" className="pt-3">
              <ContextTab orgId={orgId} run={run.data} />
            </TabsContent>
            <TabsContent value="workspace" className="pt-3">
              <WorkspaceTab orgId={orgId} run={run.data} />
            </TabsContent>
            <TabsContent value="roles" className="pt-3">
              <RolesTab orgId={orgId} run={run.data} />
            </TabsContent>
            <TabsContent value="verification" className="pt-3">
              <VerificationTab orgId={orgId} run={run.data} />
            </TabsContent>
            <TabsContent value="diff" className="pt-3">
              <DiffTab orgId={orgId} run={run.data} />
            </TabsContent>
            <TabsContent value="review" className="pt-3">
              <ReviewTab orgId={orgId} run={run.data} />
            </TabsContent>
            <TabsContent value="mergeRequest" className="pt-3">
              <MergeRequestTab orgId={orgId} run={run.data} />
            </TabsContent>
            <TabsContent value="security" className="pt-3">
              <SecurityTab orgId={orgId} run={run.data} />
            </TabsContent>
            <TabsContent value="publishing" className="pt-3">
              <PublishingTab orgId={orgId} run={run.data} />
            </TabsContent>
            <TabsContent value="history" className="pt-3">
              <HistoryTab orgId={orgId} task={task.data} run={run.data} />
            </TabsContent>
          </Tabs>
        </>
      )}
    </div>
  )
}

function Header({ orgId, task, run }: { orgId: string; task: TaskDto; run: ReturnType<typeof useRun>['data'] }) {
  const { t } = useTranslation()
  const summary = task.run
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-3">
        <h1 className="text-2xl font-semibold">
          <Ltr className="font-mono">{task.issue_key}</Ltr>
        </h1>
        <a href={task.issue_url} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 text-sm text-muted-foreground underline-offset-4 hover:underline">
          <ExternalLinkIcon className="size-3.5" aria-hidden />
          {t('taskDetail.openInJira', { key: task.issue_key })}
        </a>
        {summary ? <RunBadges run={summary} /> : task.candidate && <Badge variant="outline">{t('tasks.candidate')}</Badge>}
        <RiskBadge risk={summary?.risk} />
      </div>
      <p className="text-lg" dir="auto">
        {task.summary}
      </p>
      {summary?.eligibility === 'left_filter' && (
        <Alert>
          <AlertTitle>{t('run.eligibility.left_filter')}</AlertTitle>
          <AlertDescription>{t('run.eligibility.left_filterBody')}</AlertDescription>
        </Alert>
      )}
      {summary?.stale && (
        <Alert variant="destructive">
          <AlertTitle>{t('run.stale')}</AlertTitle>
          <AlertDescription>{t('run.staleBody')}</AlertDescription>
        </Alert>
      )}
      {run?.hold && (
        <Alert>
          <AlertTitle>{t(`run.hold.${run.hold.code}`, { defaultValue: run.hold.code })}</AlertTitle>
          <AlertDescription>{holdBody(t, run.hold.code, run.hold.detail)}</AlertDescription>
        </Alert>
      )}
      {run?.state === 'waiting_network' && (
        <Alert>
          <AlertTitle>{t('run.state.waiting_network')}</AlertTitle>
          <AlertDescription>{t('run.waitingNetworkBody')}</AlertDescription>
        </Alert>
      )}
      {run?.heads?.head && (
        <p className="flex flex-wrap items-center gap-2 text-sm" aria-label={t('taskDetail.heads')}>
          <HeadBadge label={t('taskDetail.headCode')} sha={run.heads?.head} />
          <HeadBadge label={t('taskDetail.headVerified')} sha={run.heads?.verified ?? ''} current={run.heads?.head} />
          <HeadBadge label={t('taskDetail.headReviewed')} sha={run.heads?.reviewed ?? ''} current={run.heads?.head} />
          {run.round > 0 && <Badge variant="outline">{t('taskDetail.round', { n: run.round })}</Badge>}
          {run.publish_mode && (
            <Badge variant="outline" data-publish-mode={run.publish_mode}>
              {t(run.publish_mode === 'live' ? 'publishing.badge.live' : 'publishing.badge.dry_run')}
            </Badge>
          )}
        </p>
      )}
      {run?.cancellation && (
        <p className="text-sm text-muted-foreground" role="status">
          {t(`run.cancellation.${run.cancellation.state}`)}
        </p>
      )}
      {summary?.open_inbox_id && (
        <Button asChild variant="outline" className="self-start">
          <Link to={`/orgs/${orgId}/inbox?item=${summary.open_inbox_id}`}>
            <InboxIcon />
            {t('run.openQuestion')}
          </Link>
        </Button>
      )}
      <RunActions orgId={orgId} task={task} run={run} />
    </div>
  )
}

function holdBody(t: ReturnType<typeof useTranslation>['t'], code: string, detail: string) {
  if (code === 'project_not_ready') return detail.split(', ').filter(Boolean).map((c) => readinessText(t, 'blockers', c)).join(' · ')
  return t(`run.holdBody.${code}`, { defaultValue: detail })
}

/** A head sha; it is flagged when it is not the run's current head (stale evidence). */
function HeadBadge({ label, sha, current }: { label: string; sha: string; current?: string }) {
  const { t } = useTranslation()
  const stale = !!current && sha !== current
  return (
    <Badge variant="outline" data-head-stale={stale || undefined}>
      {label}: <Ltr className="font-mono">{sha ? sha.slice(0, 8) : '-'}</Ltr>
      {stale && <span className="ms-1 text-destructive">{t('taskDetail.headStale')}</span>}
    </Badge>
  )
}
