import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useParams, useSearchParams } from 'react-router'
import { toast } from 'sonner'
import { useMergeRequest, useMergeRequests, useRefreshMergeRequestRun } from '@/api/hooks/runDetail'
import { Ltr } from '@/components/ltr'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { Switch } from '@/components/ui/switch'
import { Label } from '@/components/ui/label'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { MergeRequestView } from '@/features/tasks/merge-request-tab'
import { formatDateTime } from '@/lib/format'
import { problemMessage } from '@/lib/problem-message'
import { useIntentKey } from '@/lib/use-intent-key'
import { useLocale } from '@/lib/use-locale'

/** Every merge request of the organization, simulated and real, with the readiness of each. */
export function MergeRequestsPage() {
  const { t } = useTranslation()
  const locale = useLocale()
  const { orgId = '' } = useParams()
  const [search, setSearch] = useSearchParams()
  const readyOnly = search.get('ready') === '1'
  const mode = search.get('mode') ?? ''
  const open = search.get('mr')
  const list = useMergeRequests(orgId, { ...(readyOnly ? { ready: true } : {}), ...(mode ? { mode } : {}) })
  const set = (k: string, v: string | null) => {
    const next = new URLSearchParams(search)
    if (v) next.set(k, v)
    else next.delete(k)
    setSearch(next, { replace: true })
  }
  return (
    <div className="flex flex-col gap-4">
      <div>
        <h1 className="text-2xl font-semibold">{t('mrPage.title')}</h1>
        <p className="text-sm text-muted-foreground">{t('mrPage.subtitle')}</p>
      </div>
      <div className="flex flex-wrap items-center gap-4">
        <div className="flex items-center gap-2">
          <Switch id="mr-ready" checked={readyOnly} onCheckedChange={(c) => set('ready', c ? '1' : null)} />
          <Label htmlFor="mr-ready">{t('mrPage.readyOnly')}</Label>
        </div>
        <Select value={mode || 'all'} onValueChange={(v) => set('mode', v === 'all' ? null : v)}>
          <SelectTrigger aria-label={t('mrPage.mode')} className="w-44">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{t('mrPage.allModes')}</SelectItem>
            <SelectItem value="dry_run">{t('mr.mode.dry_run')}</SelectItem>
            <SelectItem value="live">{t('mr.mode.live')}</SelectItem>
          </SelectContent>
        </Select>
      </div>
      {list.isPending && <LoadingBlock rows={3} />}
      {list.isError && <LoadFailed title={t('mr.loadFailed')} error={list.error} onRetry={() => void list.refetch()} />}
      {list.data && list.data.length === 0 && <p className="text-sm text-muted-foreground">{t('mrPage.empty')}</p>}
      {list.data && list.data.length > 0 && (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('mrPage.columns.title')}</TableHead>
              <TableHead>{t('mrPage.columns.mode')}</TableHead>
              <TableHead>{t('mrPage.columns.state')}</TableHead>
              <TableHead>{t('mrPage.columns.pipeline')}</TableHead>
              <TableHead>{t('mrPage.columns.ready')}</TableHead>
              <TableHead>{t('mrPage.columns.updated')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {list.data.map((m) => (
              <TableRow key={m.id} data-mr-id={m.id}>
                <TableCell>
                  <Button variant="link" className="h-auto px-0 text-start" onClick={() => set('mr', m.id)}>
                    <span dir="auto">{m.title}</span>
                  </Button>
                  <div className="text-xs text-muted-foreground">
                    <Ltr>{m.source_branch}</Ltr>
                  </div>
                </TableCell>
                <TableCell>
                  <Badge variant="outline" data-mr-mode={m.mode}>
                    {t(m.mode === 'live' ? 'mr.mode.live' : 'mr.mode.dry_run')}
                  </Badge>
                </TableCell>
                <TableCell>{t(`mr.state.${m.state}`)}</TableCell>
                <TableCell>{m.pipeline ? t(`mr.pipelineStatus.${m.pipeline.status}`, { defaultValue: m.pipeline.status }) : t('mr.noPipeline')}</TableCell>
                <TableCell>
                  <Badge variant={m.readiness?.ready ? 'default' : 'outline'}>{t(m.readiness?.ready ? 'mr.ready' : 'mr.notReady')}</Badge>
                </TableCell>
                <TableCell>{formatDateTime(m.updated_at, locale)}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
      <MergeRequestSheet orgId={orgId} id={open} onClose={() => set('mr', null)} />
    </div>
  )
}

function MergeRequestSheet({ orgId, id, onClose }: { orgId: string; id: string | null; onClose: () => void }) {
  const { t } = useTranslation()
  const mr = useMergeRequest(orgId, id)
  const [key, renew] = useIntentKey()
  const refresh = useRefreshMergeRequestRun(orgId, key)
  const [pending, setPending] = useState(false)
  return (
    <Sheet open={!!id} onOpenChange={(o) => !o && onClose()}>
      <SheetContent side="end" className="overflow-y-auto sm:max-w-xl" aria-describedby="mr-sheet-desc">
        <SheetHeader>
          <SheetTitle>{t('mrPage.sheetTitle')}</SheetTitle>
          <SheetDescription id="mr-sheet-desc">{t('mrPage.sheetHelp')}</SheetDescription>
        </SheetHeader>
        <div className="flex flex-col gap-4 px-4 pb-6">
          {id && mr.isPending && <LoadingBlock rows={3} />}
          {id && mr.isError && <LoadFailed title={t('mr.loadFailed')} error={mr.error} onRetry={() => void mr.refetch()} />}
          {mr.data && (
            <>
              <MergeRequestView mr={mr.data} />
              {refresh.isError && (
                <Alert variant="destructive" role="alert">
                  <AlertDescription>{problemMessage(t, refresh.error)}</AlertDescription>
                </Alert>
              )}
              <Button
                className="self-start"
                variant="outline"
                disabled={refresh.isPending || pending || mr.data.mode !== 'live'}
                onClick={() => {
                  setPending(true)
                  refresh.mutate(
                    { orgId, mrqId: mr.data.id },
                    {
                      onSuccess: () => {
                        renew()
                        toast.success(t('mrPage.refreshing'))
                      },
                      onSettled: () => setPending(false),
                    },
                  )
                }}
              >
                {t('mrPage.refresh')}
              </Button>
            </>
          )}
        </div>
      </SheetContent>
    </Sheet>
  )
}
