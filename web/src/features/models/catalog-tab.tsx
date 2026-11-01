import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronDownIcon, RefreshCwIcon } from 'lucide-react'
import { toast } from 'sonner'
import type { CatherdCatalogRequestMode } from '@/api/generated/model'
import { useCatalog, useCatherdSchema, useCatherdStatus, useStartCatalogRefresh } from '@/api/hooks/catherd'
import { Ltr } from '@/components/ltr'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { useOperationRunner } from '@/components/use-operation-runner'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { formatDateTime, formatNumber } from '@/lib/format'
import { problemMessage } from '@/lib/problem-message'
import { useLocale } from '@/lib/use-locale'
import { RungChips } from './rung-chip'

const ALL = '__all'
const STALE_MS = 3 * 24 * 3600 * 1000

export function CatalogTab({ orgId }: { orgId: string }) {
  const { t } = useTranslation()
  const locale = useLocale()
  const schema = useCatherdSchema(orgId)
  const status = useCatherdStatus(orgId)
  const [role, setRole] = useState(ALL)
  const [backend, setBackend] = useState(ALL)
  const [text, setText] = useState('')
  const [query, setQuery] = useState('')
  const [availableOnly, setAvailableOnly] = useState(false)
  const [scoredOnly, setScoredOnly] = useState(false)
  useEffect(() => {
    const id = setTimeout(() => setQuery(text.trim()), 250)
    return () => clearTimeout(id)
  }, [text])
  const catalog = useCatalog(orgId, { ...(role !== ALL ? { role } : {}), ...(backend !== ALL ? { backend } : {}), ...(query ? { text: query } : {}), ...(availableOnly ? { available_only: true } : {}) })
  const unfiltered = useCatalog(orgId, {})
  const backends = [...new Set(unfiltered.data?.models.map((m) => m.backend) ?? [])].sort()
  const runner = useOperationRunner(orgId)
  const refresh = useStartCatalogRefresh(runner.key)
  const run = (mode: CatherdCatalogRequestMode) => refresh.mutate({ orgId, data: { mode } }, { onSuccess: runner.started, onError: (e) => toast.error(problemMessage(t, e)) })
  const unavailable = status.isSuccess && (!status.data.available || !status.data.supported)
  const refreshedAt = catalog.data?.refreshed_at
  const [now] = useState(() => Date.now())
  const stale = !!refreshedAt && now - new Date(refreshedAt).getTime() > STALE_MS
  const models = (catalog.data?.models ?? []).filter((m) => !scoredOnly || m.rungs.some((r) => r.scored))

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-3">
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="outline" disabled={unavailable || runner.running || refresh.isPending}>
              <RefreshCwIcon />
              {runner.running ? t('catalog.refreshing') : t('catalog.refresh')}
              <ChevronDownIcon />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start">
            {(['list', 'listings', 'sync'] as const).map((m) => (
              <DropdownMenuItem key={m} onSelect={() => run(m)}>
                <div className="flex flex-col">
                  <span>{t(`catalog.modes.${m}.label`)}</span>
                  <span className="text-xs text-muted-foreground">{t(`catalog.modes.${m}.help`)}</span>
                </div>
              </DropdownMenuItem>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>
        {refreshedAt ? (
          <Badge variant="outline" className={stale ? 'border-amber-500/60 text-amber-800 dark:text-amber-300' : undefined}>
            {t('catalog.refreshedAt', { time: formatDateTime(refreshedAt, locale) })}
            {stale && ` · ${t('catalog.stale')}`}
          </Badge>
        ) : (
          catalog.isSuccess && <Badge variant="outline">{t('catalog.neverBadge')}</Badge>
        )}
        {runner.failed && <span className="text-sm text-destructive">{t('catalog.refreshFailed')}</span>}
      </div>
      {unavailable && (
        <Alert>
          <AlertDescription>{t('catalog.catherdUnavailable')}</AlertDescription>
        </Alert>
      )}

      <div className="flex flex-wrap items-end gap-4">
        <div className="flex flex-col gap-2">
          <Label htmlFor="cat-search">{t('catalog.search')}</Label>
          <Input id="cat-search" type="search" className="w-60" value={text} onChange={(e) => setText(e.target.value)} placeholder={t('catalog.searchPlaceholder')} />
        </div>
        <div className="flex flex-col gap-2">
          <Label htmlFor="cat-role">{t('catalog.role')}</Label>
          <Select value={role} onValueChange={setRole}>
            <SelectTrigger id="cat-role" className="w-44">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL}>{t('catalog.allRoles')}</SelectItem>
              {schema.data?.roles.map((r) => (
                <SelectItem key={r} value={r}>
                  {t(`profile.roleNames.${r}`, { defaultValue: r })}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className="flex flex-col gap-2">
          <Label htmlFor="cat-backend">{t('catalog.backend')}</Label>
          <Select value={backend} onValueChange={setBackend}>
            <SelectTrigger id="cat-backend" className="w-44" dir="ltr">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL}>{t('catalog.allBackends')}</SelectItem>
              {backends.map((b) => (
                <SelectItem key={b} value={b}>
                  {b}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className="flex items-center gap-2">
          <Switch id="cat-avail" checked={availableOnly} onCheckedChange={setAvailableOnly} />
          <Label htmlFor="cat-avail">{t('catalog.availableOnly')}</Label>
        </div>
        <div className="flex items-center gap-2">
          <Switch id="cat-scored" checked={scoredOnly} onCheckedChange={setScoredOnly} />
          <Label htmlFor="cat-scored">{t('catalog.scoredOnly')}</Label>
        </div>
      </div>

      {catalog.isPending && <LoadingBlock />}
      {catalog.isError && <LoadFailed title={t('catalog.loadFailed')} error={catalog.error} onRetry={() => void catalog.refetch()} />}
      {catalog.isSuccess && !refreshedAt && (
        <Card>
          <CardContent className="py-6 text-sm text-muted-foreground">{t('catalog.never')}</CardContent>
        </Card>
      )}
      {catalog.isSuccess && refreshedAt && models.length === 0 && <p className="text-sm text-muted-foreground">{t('catalog.noMatches')}</p>}
      <p className="text-xs text-muted-foreground" aria-live="polite">
        {catalog.isSuccess && refreshedAt ? t('catalog.count', { count: formatNumber(models.length, locale) }) : ''}
      </p>
      <ul className="grid gap-4 lg:grid-cols-2">
        {models.map((m) => (
          <li key={m.family_id}>
            <Card className="h-full">
              <CardHeader>
                <CardTitle className="flex flex-wrap items-baseline gap-2">
                  <Ltr className="font-sans">{m.name ?? m.model}</Ltr>
                  <Ltr className="text-xs font-normal text-muted-foreground">{m.backend}</Ltr>
                </CardTitle>
                <div className="flex flex-wrap gap-1.5 text-xs text-muted-foreground">
                  <Badge variant="outline">{m.billing}</Badge>
                  {m.listed === false && <Badge variant="outline">{t('catalog.notListed')}</Badge>}
                  {m.context != null && <span>{t('catalog.context', { count: formatNumber(m.context, locale) })}</span>}
                  {m.roles.map((r) => (
                    <Badge key={r} variant="secondary">
                      {t(`profile.roleNames.${r}`, { defaultValue: r })}
                    </Badge>
                  ))}
                </div>
              </CardHeader>
              <CardContent>
                <ul className="flex flex-col divide-y">
                  {m.rungs.map((r) => (
                    <li key={r.rung} className="flex flex-col gap-1 py-2">
                      <Ltr className="break-all text-xs">{r.rung}</Ltr>
                      <RungChips rung={r} />
                      {r.availability !== 'available' && (
                        <span className="text-xs text-muted-foreground">
                          {t(`catalog.availability.${r.availability}`)}
                          {r.reasons.length > 0 && <> · {r.reasons.join(', ')}</>}
                        </span>
                      )}
                    </li>
                  ))}
                </ul>
              </CardContent>
            </Card>
          </li>
        ))}
      </ul>
    </div>
  )
}
