import { useEffect, useState } from 'react'
import { IconButton } from '@/components/icon-button'
import { useTranslation } from 'react-i18next'
import { Link, useParams } from 'react-router'
import { ExternalLinkIcon, PlayIcon, SearchIcon } from 'lucide-react'
import { toast } from 'sonner'
import { projectRows, useProjects } from '@/api/hooks/projects'
import { taskRows, useEvaluateTask, useTasks, type TaskSummary } from '@/api/hooks/tasks'
import { Ltr } from '@/components/ltr'
import { SecurityBadge } from './security-badge'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { RiskBadge, RunBadges } from '@/components/run-state-badge'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { RUN_STATES } from '@/components/run-state-badge'
import { formatDateTime } from '@/lib/format'
import { problemMessage } from '@/lib/problem-message'
import { useIntentKey } from '@/lib/use-intent-key'
import { useLocale } from '@/lib/use-locale'

const ALL = 'all'

export function TasksPage() {
  const { t } = useTranslation()
  const { orgId = '' } = useParams()
  const [project, setProject] = useState(ALL)
  const [state, setState] = useState(ALL)
  const [text, setText] = useState('')
  const [q, setQ] = useState('')
  useEffect(() => {
    const id = setTimeout(() => setQ(text.trim()), 250)
    return () => clearTimeout(id)
  }, [text])
  const projects = projectRows(useProjects(orgId, '').data)
  const nameOf = (id: string) => projects.find((p) => p.id === id)?.name ?? id
  const list = useTasks(orgId, { projectId: project === ALL ? undefined : project, state: state === ALL ? undefined : state, q: q || undefined })
  const rows = taskRows(list.data)
  const showSecurity = rows.some((r) => r.security != null)
  const filtered = project !== ALL || state !== ALL || q !== ''

  return (
    <div className="flex flex-col gap-6">
      <div>
        <h1 className="text-2xl font-semibold">{t('tasks.title')}</h1>
        <p className="text-sm text-muted-foreground">{t('tasks.subtitle')}</p>
      </div>
      <div className="flex flex-wrap items-end gap-4">
        <div className="flex min-w-48 flex-col gap-2">
          <Label htmlFor="task-project">{t('tasks.filters.project')}</Label>
          <Select value={project} onValueChange={setProject}>
            <SelectTrigger id="task-project" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL}>{t('tasks.filters.allProjects')}</SelectItem>
              {projects.map((p) => (
                <SelectItem key={p.id} value={p.id}>
                  {p.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className="flex min-w-48 flex-col gap-2">
          <Label htmlFor="task-state">{t('tasks.filters.state')}</Label>
          <Select value={state} onValueChange={setState}>
            <SelectTrigger id="task-state" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL}>{t('tasks.filters.allStates')}</SelectItem>
              <SelectItem value="candidate">{t('tasks.candidate')}</SelectItem>
              {Object.keys(RUN_STATES).map((s) => (
                <SelectItem key={s} value={s}>
                  {t(`run.state.${s}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className="flex min-w-48 flex-col gap-2">
          <Label htmlFor="task-search">{t('tasks.filters.search')}</Label>
          <div className="relative">
            <SearchIcon className="pointer-events-none absolute inset-y-0 start-2.5 my-auto size-4 text-muted-foreground" aria-hidden />
            <Input id="task-search" type="search" dir="ltr" value={text} onChange={(e) => setText(e.target.value)} placeholder={t('tasks.filters.searchPlaceholder')} className="ps-8" />
          </div>
        </div>
      </div>

      {list.isPending && <LoadingBlock />}
      {list.isError && <LoadFailed title={t('tasks.loadFailed')} error={list.error} onRetry={() => void list.refetch()} />}
      {list.isSuccess && rows.length === 0 && (
        <Card>
          <CardContent className="py-6 text-sm text-muted-foreground">{filtered ? t('tasks.emptyFiltered') : t('tasks.empty')}</CardContent>
        </Card>
      )}
      {rows.length > 0 && (
        <>
          <div className="overflow-x-auto rounded-lg border">
            <Table aria-label={t('tasks.title')}>
              <TableHeader>
                <TableRow>
                  <TableHead>{t('tasks.columns.key')}</TableHead>
                  <TableHead>{t('tasks.columns.summary')}</TableHead>
                  <TableHead>{t('tasks.columns.project')}</TableHead>
                  <TableHead>{t('tasks.columns.state')}</TableHead>
                  <TableHead>{t('tasks.columns.risk')}</TableHead>
                  {showSecurity && <TableHead>{t('tasks.columns.security')}</TableHead>}
                  <TableHead>{t('tasks.columns.updated')}</TableHead>
                  <TableHead>
                    <span className="sr-only">{t('tasks.columns.actions')}</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((task) => (
                  <TaskRow key={task.id} orgId={orgId} task={task} projectName={nameOf(task.project_id)} showSecurity={showSecurity} />
                ))}
              </TableBody>
            </Table>
          </div>
          {list.hasNextPage && (
            <div>
              <Button variant="outline" onClick={() => void list.fetchNextPage()} disabled={list.isFetchingNextPage}>
                {t('tasks.loadMore')}
              </Button>
            </div>
          )}
        </>
      )}
    </div>
  )
}

function TaskRow({ orgId, task, projectName, showSecurity }: { orgId: string; task: TaskSummary; projectName: string; showSecurity: boolean }) {
  const { t } = useTranslation()
  const locale = useLocale()
  const run = task.run
  return (
    <TableRow>
      <TableCell className="whitespace-nowrap font-medium">
        <Link to={`/orgs/${orgId}/tasks/${task.id}`} className="underline-offset-4 outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring">
          <Ltr>{task.issue_key}</Ltr>
        </Link>
        <IconButton asChild variant="ghost" size="icon-xs" className="ms-1.5 align-middle text-muted-foreground" aria-label={t('taskDetail.openInJira', { key: task.issue_key })}>
          <a href={task.issue_url} target="_blank" rel="noreferrer">
            <ExternalLinkIcon className="size-3.5" aria-hidden />
          </a>
        </IconButton>
      </TableCell>
      <TableCell className="max-w-md whitespace-normal">
        <span dir="auto">{task.summary}</span>
        {task.ambiguous_project.length > 1 && <Badge variant="destructive" className="ms-2">{t('tasks.ambiguousProject')}</Badge>}
        {task.inaccessible && <Badge variant="destructive" className="ms-2">{t('tasks.inaccessible')}</Badge>}
        {!task.in_filter && <Badge variant="outline" className="ms-2">{t('tasks.leftFilter')}</Badge>}
      </TableCell>
      <TableCell className="whitespace-normal">{projectName}</TableCell>
      <TableCell className="whitespace-normal">
        {run ? <RunBadges run={run} /> : task.candidate ? <Badge variant="outline">{t('tasks.candidate')}</Badge> : <span className="text-muted-foreground">{t('tasks.noRun')}</span>}
      </TableCell>
      <TableCell>
        <RiskBadge risk={run?.risk} />
      </TableCell>
      {showSecurity && (
        <TableCell className="whitespace-nowrap">
          <SecurityBadge security={task.security} />
        </TableCell>
      )}
      <TableCell className="whitespace-nowrap text-xs text-muted-foreground">{formatDateTime(run?.updated_at ?? task.last_seen_at, locale)}</TableCell>
      <TableCell className="whitespace-nowrap">
        {task.candidate && !task.inaccessible && <EvaluateButton orgId={orgId} task={task} />}
      </TableCell>
    </TableRow>
  )
}

export function EvaluateButton({ orgId, task, size = 'sm' }: { orgId: string; task: Pick<TaskSummary, 'id' | 'issue_key'>; size?: 'sm' | 'default' }) {
  const { t } = useTranslation()
  const [key, renew] = useIntentKey()
  const evaluate = useEvaluateTask(orgId, key)
  return (
    <Button
      size={size}
      variant="outline"
      disabled={evaluate.isPending}
      aria-label={t('tasks.evaluateLabel', { key: task.issue_key })}
      onClick={() =>
        evaluate.mutate(
          { orgId, issId: task.id },
          {
            onSuccess: () => {
              renew()
              toast.success(t('tasks.evaluated', { key: task.issue_key }))
            },
            onError: (e) => toast.error(problemMessage(t, e)),
          },
        )
      }
    >
      <PlayIcon />
      {t('run.actions.evaluate')}
    </Button>
  )
}
