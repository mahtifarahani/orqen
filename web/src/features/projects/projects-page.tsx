import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link, useParams } from 'react-router'
import { SearchIcon, TelescopeIcon } from 'lucide-react'
import { projectRows, useProjects } from '@/api/hooks/projects'
import { Ltr } from '@/components/ltr'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { formatNumber } from '@/lib/format'
import { problemMessage } from '@/lib/problem-message'
import { useLocale } from '@/lib/use-locale'
import { ProjectStateBadge } from './project-state-badge'

export function ProjectsPage() {
  const { t } = useTranslation()
  const locale = useLocale()
  const { orgId = '' } = useParams()
  const [text, setText] = useState('')
  const [search, setSearch] = useState('')
  useEffect(() => {
    const id = setTimeout(() => setSearch(text.trim()), 250)
    return () => clearTimeout(id)
  }, [text])
  const list = useProjects(orgId, search)
  const rows = projectRows(list.data)

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold">{t('projects.title')}</h1>
          <p className="text-sm text-muted-foreground">{t('projects.subtitle')}</p>
        </div>
        <Button asChild>
          <Link to={`/orgs/${orgId}/projects/discover`}>
            <TelescopeIcon />
            {t('projects.discover')}
          </Link>
        </Button>
      </div>

      <div className="flex max-w-sm flex-col gap-2">
        <Label htmlFor="project-search">{t('projects.searchLabel')}</Label>
        <div className="relative">
          <SearchIcon className="pointer-events-none absolute inset-y-0 start-2.5 my-auto size-4 text-muted-foreground" aria-hidden />
          <Input id="project-search" type="search" value={text} onChange={(e) => setText(e.target.value)} placeholder={t('projects.searchPlaceholder')} className="ps-8" />
        </div>
      </div>

      {list.isPending && (
        <div className="flex flex-col gap-2" role="status" aria-label={t('app.loading')}>
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
      )}
      {list.isError && (
        <Alert variant="destructive">
          <AlertTitle>{t('projects.loadFailed')}</AlertTitle>
          <AlertDescription className="flex flex-col items-start gap-2">
            {problemMessage(t, list.error)}
            <Button size="sm" variant="outline" onClick={() => void list.refetch()}>
              {t('common.retry')}
            </Button>
          </AlertDescription>
        </Alert>
      )}
      {list.isSuccess && rows.length === 0 && (
        <Card>
          <CardContent className="flex flex-col items-start gap-4 py-6">
            <p className="max-w-prose text-sm text-muted-foreground">{search ? t('projects.noMatches') : t('projects.empty')}</p>
            {!search && (
              <Button asChild>
                <Link to={`/orgs/${orgId}/projects/discover`}>
                  <TelescopeIcon />
                  {t('projects.discover')}
                </Link>
              </Button>
            )}
          </CardContent>
        </Card>
      )}
      {rows.length > 0 && (
        <>
          <div className="overflow-x-auto rounded-lg border">
            <Table aria-label={t('projects.title')}>
              <TableHeader>
                <TableRow>
                  <TableHead>{t('projects.columns.name')}</TableHead>
                  <TableHead>{t('projects.columns.repository')}</TableHead>
                  <TableHead>{t('projects.columns.state')}</TableHead>
                  <TableHead>{t('projects.columns.blockers')}</TableHead>
                  <TableHead>{t('projects.columns.automation')}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((p) => (
                  <TableRow key={p.id}>
                    <TableCell className="font-medium">
                      <Link to={`/orgs/${orgId}/projects/${p.id}`} className="underline-offset-4 outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring">
                        {p.name}
                      </Link>
                    </TableCell>
                    <TableCell className="whitespace-normal break-all text-xs">{p.primary_repo ? <Ltr>{p.primary_repo.path}</Ltr> : <span className="text-muted-foreground">{t('common.none')}</span>}</TableCell>
                    <TableCell>
                      <ProjectStateBadge state={p.state} />
                    </TableCell>
                    <TableCell className="tabular-nums">{formatNumber(p.blocker_count, locale)}</TableCell>
                    <TableCell>
                      <Badge variant={p.automation_enabled ? 'default' : 'outline'}>{p.automation_enabled ? t('projects.automation.on') : t('projects.automation.off')}</Badge>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
          {list.hasNextPage && (
            <div>
              <Button variant="outline" onClick={() => void list.fetchNextPage()} disabled={list.isFetchingNextPage}>
                {t('projects.loadMore')}
              </Button>
            </div>
          )}
        </>
      )}
    </div>
  )
}
