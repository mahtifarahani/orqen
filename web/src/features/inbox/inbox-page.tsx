import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link, useParams, useSearchParams } from 'react-router'
import { projectRows, useProjects } from '@/api/hooks/projects'
import { inboxRows, useInbox } from '@/api/hooks/inbox'
import { Ltr } from '@/components/ltr'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { formatDateTime } from '@/lib/format'
import { useLocale } from '@/lib/use-locale'
import { InboxItemSheet, questionText } from './inbox-item-sheet'

const ALL = 'all'
export const INBOX_REASONS = ['missing_acceptance', 'ambiguous_behavior', 'project_mapping', 'design_access', 'policy_conflict', 'protected_change', 'unsupported_toolchain', 'model_unavailable', 'budget_exceeded', 'credential_expired', 'review_disagreement', 'external_write_uncertain', 'dependency_unavailable', 'plan_approval']

export function InboxPage() {
  const { t } = useTranslation()
  const locale = useLocale()
  const { orgId = '' } = useParams()
  const [search, setSearch] = useSearchParams()
  const itemId = search.get('item')
  const [project, setProject] = useState(ALL)
  const [priority, setPriority] = useState(ALL)
  const [reason, setReason] = useState(ALL)
  const [status, setStatus] = useState('open')
  const projects = projectRows(useProjects(orgId, '').data)
  const list = useInbox(orgId, { projectId: project === ALL ? undefined : project, priority: priority === ALL ? undefined : priority, reason: reason === ALL ? undefined : reason, status })
  const rows = inboxRows(list.data)
  const select = (label: string, id: string, value: string, set: (v: string) => void, options: [string, string][]) => (
    <div className="flex min-w-44 flex-col gap-2">
      <Label htmlFor={id}>{label}</Label>
      <Select value={value} onValueChange={set}>
        <SelectTrigger id={id} className="w-full">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {options.map(([v, l]) => (
            <SelectItem key={v} value={v}>
              {l}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  )
  const open = (id: string | null) => setSearch(id ? { item: id } : {}, { replace: true })

  return (
    <div className="flex flex-col gap-6">
      <div>
        <h1 className="text-2xl font-semibold">{t('inbox.title')}</h1>
        <p className="text-sm text-muted-foreground">{t('inbox.subtitle')}</p>
      </div>
      <div className="flex flex-wrap items-end gap-4">
        {select(t('inbox.filters.project'), 'inbox-project', project, setProject, [[ALL, t('inbox.filters.allProjects')], ...projects.map((p): [string, string] => [p.id, p.name])])}
        {select(t('inbox.filters.priority'), 'inbox-priority', priority, setPriority, [[ALL, t('inbox.filters.allPriorities')], ...(['high', 'normal', 'low'] as const).map((p): [string, string] => [p, t(`inbox.priority.${p}`)])])}
        {select(t('inbox.filters.reason'), 'inbox-reason', reason, setReason, [[ALL, t('inbox.filters.allReasons')], ...INBOX_REASONS.map((r): [string, string] => [r, t(`inbox.reason.${r}`)])])}
        {select(t('inbox.filters.status'), 'inbox-status', status, setStatus, (['open', 'resolved', 'superseded', 'cancelled'] as const).map((s): [string, string] => [s, t(`inbox.status.${s}`)]))}
      </div>
      {list.isPending && <LoadingBlock />}
      {list.isError && <LoadFailed title={t('inbox.loadFailed')} error={list.error} onRetry={() => void list.refetch()} />}
      {list.isSuccess && rows.length === 0 && (
        <Card>
          <CardContent className="py-6 text-sm text-muted-foreground">{t('inbox.empty')}</CardContent>
        </Card>
      )}
      {rows.length > 0 && (
        <>
          <div className="overflow-x-auto rounded-lg border">
            <Table aria-label={t('inbox.title')}>
              <TableHeader>
                <TableRow>
                  <TableHead>{t('inbox.columns.reason')}</TableHead>
                  <TableHead>{t('inbox.columns.question')}</TableHead>
                  <TableHead>{t('inbox.columns.priority')}</TableHead>
                  <TableHead>{t('inbox.columns.task')}</TableHead>
                  <TableHead>{t('inbox.columns.stage')}</TableHead>
                  <TableHead>{t('inbox.columns.age')}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((it) => (
                  <TableRow key={it.id}>
                    <TableCell className="whitespace-normal">
                      <Badge variant="outline">{t(`inbox.reason.${it.reason}`, { defaultValue: it.reason })}</Badge>
                    </TableCell>
                    <TableCell className="max-w-sm whitespace-normal">
                      <button type="button" onClick={() => open(it.id)} className="text-start underline-offset-4 outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring" dir="auto">
                        {questionText(t, it)}
                      </button>
                    </TableCell>
                    <TableCell>
                      <Badge variant={it.priority === 'high' ? 'destructive' : 'secondary'}>{t(`inbox.priority.${it.priority}`)}</Badge>
                    </TableCell>
                    <TableCell>
                      <Link to={`/orgs/${orgId}/tasks/${it.task_id}`} className="underline underline-offset-4">
                        <Ltr>{it.issue_key}</Ltr>
                      </Link>
                    </TableCell>
                    <TableCell>{t(`run.stage.${it.stage}`, { defaultValue: it.stage })}</TableCell>
                    <TableCell className="whitespace-nowrap text-xs text-muted-foreground">{formatDateTime(it.created_at, locale)}</TableCell>
                  </TableRow>
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
      <InboxItemSheet orgId={orgId} id={itemId} onClose={() => open(null)} />
    </div>
  )
}
