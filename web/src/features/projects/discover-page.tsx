import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link, useParams } from 'react-router'
import { toast } from 'sonner'
import { Loader2Icon, RefreshCwIcon, SearchIcon } from 'lucide-react'
import { catalogRows, useCatalogProjects, useCatalogSummary, useRefreshCatalog } from '@/api/hooks/catalog'
import { useConnections } from '@/api/hooks/connections'
import { useOrg } from '@/api/hooks/orgs'
import { useAddFromCatalog } from '@/api/hooks/projects'
import type { ListCatalogProjectsSelection } from '@/api/generated/model'
import { newIdempotencyKey } from '@/api/idempotency'
import { ApiProblem, ok } from '@/api/problem'
import { useEventFeed } from '@/components/events-context'
import { useOperationRunner } from '@/components/use-operation-runner'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { formatDateTime, formatNumber } from '@/lib/format'
import { problemMessage } from '@/lib/problem-message'
import { useLocale } from '@/lib/use-locale'
import { isStale } from './catalog-selection'
import { CatalogTable } from './catalog-table'

function operationProblem(p: unknown) {
  const problem = (p ?? {}) as { code?: string; message?: string }
  return new ApiProblem({ status: 500, code: problem.code ?? 'unknown', detail: problem.message ?? '' })
}

const FILTERS: ListCatalogProjectsSelection[] = ['all', 'selected', 'unselected']

export function DiscoverPage({ embedded = false }: { embedded?: boolean } = {}) {
  const { t } = useTranslation()
  const locale = useLocale()
  const { orgId = '' } = useParams()
  const org = useOrg(orgId)
  const connections = useConnections(orgId)
  const gitlab = useMemo(() => connections.data?.filter((c) => c.provider === 'gitlab') ?? [], [connections.data])
  const [chosenConnection, setConnection] = useState<string>()
  const conId = chosenConnection && gitlab.some((c) => c.id === chosenConnection) ? chosenConnection : gitlab[0]?.id

  const [text, setText] = useState('')
  const [search, setSearch] = useState('')
  useEffect(() => {
    const id = setTimeout(() => setSearch(text.trim()), 250)
    return () => clearTimeout(id)
  }, [text])
  const [selection, setSelection] = useState<ListCatalogProjectsSelection>('all')
  const [includeArchived, setIncludeArchived] = useState(false)

  const catalog = useCatalogProjects(orgId, conId, {
    ...(search ? { search } : {}),
    ...(selection !== 'all' ? { selection } : {}),
    ...(includeArchived ? { include_archived: true } : {}),
  })
  const rows = catalogRows(catalog.data)
  const summary = useCatalogSummary(orgId)
  const info = summary.data?.find((s) => s.connection_id === conId)

  // The chosen projects survive paging and filtering; they are cleared after a successful add.
  const [chosen, setChosen] = useState<{ conId?: string; ids: Set<string> }>({ ids: new Set() })
  const selected = chosen.conId === conId ? chosen.ids : new Set<string>()
  const [addKey, setAddKey] = useState(newIdempotencyKey)
  const change = (next: Set<string>) => {
    setChosen({ conId, ids: next })
    setAddKey(newIdempotencyKey()) // a different selection is a different request
  }
  const toggle = (id: string) => {
    const next = new Set(selected)
    if (!next.delete(id)) next.add(id)
    change(next)
  }
  const toggleAll = (ids: string[], checked: boolean) => {
    const next = new Set(selected)
    for (const id of ids) {
      if (checked) next.add(id)
      else next.delete(id)
    }
    change(next)
  }

  const runner = useOperationRunner(orgId)
  const refresh = useRefreshCatalog(runner.key)
  const feed = useEventFeed()
  const seen = [...feed.events].reverse().find((e) => e.type === 'catalog_refresh.progress' && e.operation_id === runner.opId)?.payload?.seen
  const result = runner.operation?.result as { seen?: number; added?: number; gone?: number } | null | undefined
  const refreshing = refresh.isPending || runner.running

  const add = useAddFromCatalog(orgId, addKey)
  const submit = () => {
    if (!conId || selected.size === 0) return
    add.mutate(
      { orgId, data: { connection_id: conId, provider_project_ids: [...selected] } },
      {
        onSuccess: (res) => {
          const r = ok(res)
          toast.success(t('discover.added', { created: r.created.length, existing: r.existing.length, missing: r.missing.length }))
          setChosen({ conId, ids: new Set() })
          setAddKey(newIdempotencyKey())
        },
      },
    )
  }

  const stale = isStale(info?.refreshed_at)

  return (
    <div className="flex flex-col gap-6">
      {embedded ? (
        <p className="max-w-prose text-sm text-muted-foreground">{t('discover.subtitle')}</p>
      ) : (
        <div>
          <h1 className="text-2xl font-semibold">{t('discover.title')}</h1>
          <p className="max-w-prose text-sm text-muted-foreground">{t('discover.subtitle')}</p>
        </div>
      )}

      {connections.isPending && <Skeleton className="h-10 w-full" role="status" aria-label={t('app.loading')} />}
      {connections.isError && (
        <Alert variant="destructive">
          <AlertTitle>{t('connections.loadFailed')}</AlertTitle>
          <AlertDescription>{problemMessage(t, connections.error)}</AlertDescription>
        </Alert>
      )}
      {connections.isSuccess && gitlab.length === 0 && (
        <Card>
          <CardContent className="flex flex-col items-start gap-4 py-6">
            <p className="text-sm text-muted-foreground">{t('discover.noConnection')}</p>
            <Button asChild>
              <Link to={`/orgs/${orgId}/connections`}>{t('discover.goConnections')}</Link>
            </Button>
          </CardContent>
        </Card>
      )}

      {conId && (
        <>
          <div className="flex flex-wrap items-end gap-4">
            <div className="flex min-w-48 flex-col gap-2">
              <Label htmlFor="discover-connection">{t('discover.connection')}</Label>
              <Select value={conId} onValueChange={setConnection}>
                <SelectTrigger id="discover-connection" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {gitlab.map((c) => (
                    <SelectItem key={c.id} value={c.id}>
                      {c.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <Button
              variant="outline"
              disabled={refreshing}
              onClick={() =>
                refresh.mutate(
                  { orgId, conId },
                  {
                    onSuccess: (res) => {
                      runner.started(res)
                      if (ok(res).deduplicated) toast.info(t('discover.deduplicated'))
                    },
                  },
                )
              }
            >
              {refreshing ? <Loader2Icon className="animate-spin" /> : <RefreshCwIcon />}
              {refreshing ? t('discover.refreshing') : t('discover.refresh')}
            </Button>
            <div className="flex flex-wrap items-center gap-2 text-sm text-muted-foreground">
              {info && <span>{t('discover.projectCount', { count: info.project_count })}</span>}
              <Badge variant={stale ? 'outline' : 'secondary'} className={stale ? 'border-amber-500/60 text-amber-800 dark:text-amber-300' : undefined}>
                {info?.refreshed_at ? t('discover.lastRefreshed', { date: formatDateTime(info.refreshed_at, locale, org.data?.timezone) }) : t('discover.neverRefreshed')}
              </Badge>
              {stale && info?.refreshed_at && <span>{t('discover.staleCatalog')}</span>}
            </div>
          </div>
          <div role="status" aria-live="polite" className="text-sm">
            {refreshing && typeof seen === 'number' && t('discover.refreshingProgress', { count: seen })}
          </div>
          {refresh.isError && (
            <Alert variant="destructive" role="alert">
              <AlertDescription>{problemMessage(t, refresh.error)}</AlertDescription>
            </Alert>
          )}
          {runner.failed && (
            <Alert variant="destructive" role="alert">
              <AlertTitle>{t('discover.refreshFailed')}</AlertTitle>
              <AlertDescription>{problemMessage(t, operationProblem(runner.operation?.problem))}</AlertDescription>
            </Alert>
          )}
          {runner.succeeded && result && (
            <Alert role="status">
              <AlertDescription>{t('discover.refreshed', { seen: formatNumber(result.seen ?? 0, locale), added: formatNumber(result.added ?? 0, locale), gone: formatNumber(result.gone ?? 0, locale) })}</AlertDescription>
            </Alert>
          )}

          <div className="flex flex-wrap items-end gap-4">
            <div className="flex min-w-56 flex-col gap-2">
              <Label htmlFor="discover-search">{t('discover.searchLabel')}</Label>
              <div className="relative">
                <SearchIcon className="pointer-events-none absolute inset-y-0 start-2.5 my-auto size-4 text-muted-foreground" aria-hidden />
                <Input id="discover-search" type="search" value={text} onChange={(e) => setText(e.target.value)} placeholder={t('discover.searchPlaceholder')} className="ps-8" />
              </div>
            </div>
            <div className="flex min-w-40 flex-col gap-2">
              <Label htmlFor="discover-filter">{t('discover.filter.label')}</Label>
              <Select value={selection} onValueChange={(v) => setSelection(v as ListCatalogProjectsSelection)}>
                <SelectTrigger id="discover-filter" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {FILTERS.map((f) => (
                    <SelectItem key={f} value={f}>
                      {t(`discover.filter.${f}`)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="flex items-center gap-2 pb-1">
              <Checkbox id="discover-archived" checked={includeArchived} onCheckedChange={(v) => setIncludeArchived(v === true)} />
              <Label htmlFor="discover-archived">{t('discover.includeArchived')}</Label>
            </div>
          </div>

          {catalog.isPending && (
            <div className="flex flex-col gap-2" role="status" aria-label={t('app.loading')}>
              <Skeleton className="h-10 w-full" />
              <Skeleton className="h-10 w-full" />
              <Skeleton className="h-10 w-full" />
            </div>
          )}
          {catalog.isError && (
            <Alert variant="destructive">
              <AlertTitle>{t('discover.loadFailed')}</AlertTitle>
              <AlertDescription className="flex flex-col items-start gap-2">
                {problemMessage(t, catalog.error)}
                <Button size="sm" variant="outline" onClick={() => void catalog.refetch()}>
                  {t('common.retry')}
                </Button>
              </AlertDescription>
            </Alert>
          )}
          {catalog.isSuccess && rows.length === 0 && (
            <Card>
              <CardContent className="flex flex-col items-start gap-2 py-6">
                {search || selection !== 'all' ? (
                  <p className="text-sm text-muted-foreground">{t('discover.noResults')}</p>
                ) : (
                  <>
                    <p className="font-medium">{t('discover.emptyCatalog')}</p>
                    <p className="text-sm text-muted-foreground">{t('discover.emptyCatalogBody')}</p>
                  </>
                )}
              </CardContent>
            </Card>
          )}
          {rows.length > 0 && (
            <>
              <CatalogTable orgId={orgId} rows={rows} selected={selected} onToggle={toggle} onToggleAll={toggleAll} timeZone={org.data?.timezone} />
              <div className="flex flex-wrap items-center gap-3">
                {catalog.hasNextPage && (
                  <Button variant="outline" onClick={() => void catalog.fetchNextPage()} disabled={catalog.isFetchingNextPage}>
                    {t('discover.loadMore')}
                  </Button>
                )}
                <span className="text-xs text-muted-foreground">{t('discover.loaded', { count: rows.length })}</span>
              </div>
            </>
          )}
          {add.isError && (
            <Alert variant="destructive" role="alert">
              <AlertDescription>{problemMessage(t, add.error)}</AlertDescription>
            </Alert>
          )}
          {selected.size > 0 && (
            <div role="region" aria-label={t('discover.selectedCount', { count: selected.size })} className="sticky bottom-0 -mx-4 flex flex-wrap items-center justify-between gap-3 border-t bg-background/95 px-4 py-3 backdrop-blur md:-mx-6 md:px-6">
              <span className="text-sm font-medium">{t('discover.selectedCount', { count: selected.size })}</span>
              <div className="flex gap-2">
                <Button variant="ghost" onClick={() => change(new Set())} disabled={add.isPending}>
                  {t('discover.clear')}
                </Button>
                <Button onClick={submit} disabled={add.isPending}>
                  {add.isPending ? <Loader2Icon className="animate-spin" /> : null}
                  {add.isPending ? t('discover.adding') : t('discover.add', { count: selected.size })}
                </Button>
              </div>
            </div>
          )}
        </>
      )}
    </div>
  )
}
