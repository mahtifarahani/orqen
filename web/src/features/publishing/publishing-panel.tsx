import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import type { ExternalWriteAllowDto } from '@/api/generated/model'
import { catalogRows, useCatalogProjects } from '@/api/hooks/catalog'
import { useConnections } from '@/api/hooks/connections'
import { LIVE_PHRASE, usePauseExternalWrites, usePutExternalWrites, useExternalWrites } from '@/api/hooks/externalWrites'
import { ApiProblem } from '@/api/problem'
import { Field } from '@/components/field'
import { Ltr } from '@/components/ltr'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { problemMessage } from '@/lib/problem-message'
import { HostCard } from './host-card'
import { WriteCredentialCard } from './write-credential-card'

/** Settings, Publishing tab: the write mode (dry-run by default), the allowlist, pause, the environment guard. */
export function PublishingPanel({ orgId }: { orgId: string }) {
  const { t } = useTranslation()
  const settings = useExternalWrites(orgId)
  const conns = useConnections(orgId)
  if (settings.isPending) return <LoadingBlock />
  if (settings.isError) return <LoadFailed title={t('publishing.loadFailed')} error={settings.error} onRetry={() => void settings.refetch()} />
  const gitlab = (conns.data ?? []).filter((c) => c.provider === 'gitlab')
  return (
    <div className="flex max-w-3xl flex-col gap-6">
      <ModeCard orgId={orgId} />
      <AllowlistCard orgId={orgId} />
      <PauseCard orgId={orgId} />
      <GuardCard orgId={orgId} />
      <div className="flex flex-col gap-3">
        <h2 className="text-lg font-semibold">{t('writeCredential.title')}</h2>
        {gitlab.length === 0 && <p className="text-sm text-muted-foreground">{t('writeCredential.noGitlab')}</p>}
        {gitlab.map((c) => (
          <WriteCredentialCard key={c.id} orgId={orgId} connection={c} />
        ))}
      </div>
      <HostCard orgId={orgId} />
    </div>
  )
}

const toInput = (a: ExternalWriteAllowDto) => ({ connection_id: a.connection_id, provider_project_id: a.provider_project_id })

function ModeCard({ orgId }: { orgId: string }) {
  const { t } = useTranslation()
  const { data: s } = useExternalWrites(orgId)
  const put = usePutExternalWrites(orgId, s?.revision ?? 0)
  const [open, setOpen] = useState(false)
  const [phrase, setPhrase] = useState('')
  if (!s) return null
  const live = s.mode === 'live'
  const phraseError = put.error instanceof ApiProblem && put.error.code === 'confirm_phrase_mismatch'
  const close = (o: boolean) => {
    setOpen(o)
    if (!o) {
      setPhrase('')
      put.reset()
    }
  }
  return (
    <Card>
      <CardContent className="flex flex-col gap-3 p-4">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 className="text-lg font-semibold">{t('publishing.mode.title')}</h2>
          <Badge variant="outline" data-write-mode={live ? 'live' : 'dry_run'}>
            {t(live ? 'publishing.badge.live' : 'publishing.badge.dry_run')}
          </Badge>
        </div>
        <p className="text-sm text-muted-foreground">{t(live ? 'publishing.mode.liveHelp' : 'publishing.mode.dryHelp')}</p>
        {put.isError && !phraseError && !open && (
          <Alert variant="destructive" role="alert">
            <AlertDescription>{problemMessage(t, put.error)}</AlertDescription>
          </Alert>
        )}
        <div className="flex flex-wrap gap-2">
          {live ? (
            <Button
              variant="outline"
              disabled={put.isPending}
              onClick={() => put.mutate({ orgId, data: { mode: 'dry_run', allowlist: s.allowlist.map(toInput), confirm_phrase: null } })}
            >
              {t('publishing.mode.backToDry')}
            </Button>
          ) : (
            <Button variant="destructive" onClick={() => setOpen(true)}>
              {t('publishing.mode.enableLive')}
            </Button>
          )}
        </div>
        <Dialog open={open} onOpenChange={close}>
          <DialogContent>
            <form
              onSubmit={(e) => {
                e.preventDefault()
                put.mutate(
                  { orgId, data: { mode: 'live', allowlist: s.allowlist.map(toInput), confirm_phrase: phrase } },
                  {
                    onSuccess: () => {
                      close(false)
                      toast.success(t('publishing.mode.liveEnabled'))
                    },
                  },
                )
              }}
              className="flex flex-col gap-4"
            >
              <DialogHeader>
                <DialogTitle>{t('publishing.liveDialog.title')}</DialogTitle>
                <DialogDescription>{t('publishing.liveDialog.description')}</DialogDescription>
              </DialogHeader>
              <ul className="list-disc ps-5 text-sm">
                <li>{t('publishing.liveDialog.pointAllowlist')}</li>
                <li>{t('publishing.liveDialog.pointCredential')}</li>
                <li>{t('publishing.liveDialog.pointApproval')}</li>
              </ul>
              <Field id="live-phrase" label={t('publishing.liveDialog.phraseLabel', { phrase: LIVE_PHRASE })} error={phraseError ? t('publishing.liveDialog.phraseMismatch') : undefined}>
                {(f) => <Input {...f} dir="ltr" autoComplete="off" value={phrase} onChange={(e) => setPhrase(e.target.value)} />}
              </Field>
              {put.isError && !phraseError && (
                <Alert variant="destructive" role="alert">
                  <AlertDescription>{problemMessage(t, put.error)}</AlertDescription>
                </Alert>
              )}
              <DialogFooter>
                <Button type="button" variant="ghost" onClick={() => close(false)}>
                  {t('common.cancel')}
                </Button>
                <Button type="submit" variant="destructive" disabled={put.isPending || phrase !== LIVE_PHRASE}>
                  {t('publishing.liveDialog.confirm')}
                </Button>
              </DialogFooter>
            </form>
          </DialogContent>
        </Dialog>
      </CardContent>
    </Card>
  )
}

function AllowlistCard({ orgId }: { orgId: string }) {
  const { t } = useTranslation()
  const { data: s } = useExternalWrites(orgId)
  const conns = useConnections(orgId)
  const put = usePutExternalWrites(orgId, s?.revision ?? 0)
  const [conId, setConId] = useState('')
  const [search, setSearch] = useState('')
  const gitlab = (conns.data ?? []).filter((c) => c.provider === 'gitlab')
  const cat = useCatalogProjects(orgId, conId || undefined, { search: search || undefined })
  if (!s) return null
  const rows = catalogRows(cat.data).filter((r) => !s.allowlist.some((a) => a.connection_id === r.connection_id && a.provider_project_id === r.provider_project_id))
  const save = (list: { connection_id: string; provider_project_id: string }[]) =>
    put.mutate({ orgId, data: { mode: s.mode, allowlist: list, confirm_phrase: null } })
  return (
    <Card>
      <CardContent className="flex flex-col gap-3 p-4">
        <h2 className="text-lg font-semibold">{t('publishing.allowlist.title')}</h2>
        <p className="text-sm text-muted-foreground">{t('publishing.allowlist.help')}</p>
        {s.allowlist.length === 0 ? (
          <p className="text-sm">{t('publishing.allowlist.empty')}</p>
        ) : (
          <ul className="flex flex-col gap-2" aria-label={t('publishing.allowlist.title')}>
            {s.allowlist.map((a) => (
              <li key={`${a.connection_id}/${a.provider_project_id}`} className="flex items-center justify-between gap-2 rounded-md border p-2 text-sm">
                <Ltr>
                  {a.path_snapshot} <span className="text-muted-foreground">({a.host_snapshot})</span>
                </Ltr>
                <Button size="sm" variant="ghost" disabled={put.isPending} onClick={() => save(s.allowlist.filter((x) => x !== a).map(toInput))}>
                  {t('publishing.allowlist.remove')}
                </Button>
              </li>
            ))}
          </ul>
        )}
        <div className="flex flex-col gap-2 border-t pt-3">
          <Field id="allow-conn" label={t('publishing.allowlist.connection')}>
            {(f) => (
              <Select value={conId} onValueChange={setConId}>
                <SelectTrigger {...f} className="w-full">
                  <SelectValue placeholder={t('publishing.allowlist.chooseConnection')} />
                </SelectTrigger>
                <SelectContent>
                  {gitlab.map((c) => (
                    <SelectItem key={c.id} value={c.id}>
                      {c.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}
          </Field>
          {conId && (
            <>
              <Field id="allow-search" label={t('publishing.allowlist.search')}>
                {(f) => <Input {...f} dir="ltr" value={search} onChange={(e) => setSearch(e.target.value)} />}
              </Field>
              <ul className="flex max-h-48 flex-col gap-1 overflow-auto" aria-label={t('publishing.allowlist.candidates')}>
                {rows.map((r) => (
                  <li key={r.id} className="flex items-center justify-between gap-2 text-sm">
                    <Ltr>{r.path_with_namespace}</Ltr>
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={put.isPending}
                      onClick={() => save([...s.allowlist.map(toInput), { connection_id: r.connection_id, provider_project_id: r.provider_project_id }])}
                    >
                      {t('publishing.allowlist.add')}
                    </Button>
                  </li>
                ))}
              </ul>
            </>
          )}
        </div>
        {put.isError && (
          <Alert variant="destructive" role="alert">
            <AlertDescription>{problemMessage(t, put.error)}</AlertDescription>
          </Alert>
        )}
      </CardContent>
    </Card>
  )
}

function PauseCard({ orgId }: { orgId: string }) {
  const { t } = useTranslation()
  const { data: s } = useExternalWrites(orgId)
  const pause = usePauseExternalWrites(orgId, s?.revision ?? 0)
  if (!s) return null
  return (
    <Card>
      <CardContent className="flex flex-col gap-3 p-4">
        <div className="flex items-center justify-between gap-3">
          <div>
            <h2 className="text-lg font-semibold">{t('publishing.pause.title')}</h2>
            <p className="text-sm text-muted-foreground">{t('publishing.pause.help')}</p>
          </div>
          <Switch
            aria-label={t('publishing.pause.title')}
            checked={s.paused}
            disabled={pause.isPending}
            onCheckedChange={(paused) => pause.mutate({ orgId, data: { paused, reason: null } })}
          />
        </div>
        {pause.isError && (
          <Alert variant="destructive" role="alert">
            <AlertDescription>{problemMessage(t, pause.error)}</AlertDescription>
          </Alert>
        )}
      </CardContent>
    </Card>
  )
}

function GuardCard({ orgId }: { orgId: string }) {
  const { t } = useTranslation()
  const { data: s } = useExternalWrites(orgId)
  if (!s) return null
  const g = s.env_guard
  return (
    <Card>
      <CardContent className="flex flex-col gap-2 p-4">
        <h2 className="text-lg font-semibold">{t('publishing.guard.title')}</h2>
        <p className="text-sm text-muted-foreground">{t('publishing.guard.help')}</p>
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
          <dt className="text-muted-foreground">{t('publishing.guard.channel')}</dt>
          <dd><Ltr>{g.build_channel}</Ltr></dd>
          <dt className="text-muted-foreground">{t('publishing.guard.env')}</dt>
          <dd><Ltr>{g.orqen_env || '-'}</Ltr></dd>
          <dt className="text-muted-foreground">{t('publishing.guard.scope')}</dt>
          <dd><Ltr>{g.scope}</Ltr></dd>
          <dt className="text-muted-foreground">{t('publishing.guard.killSwitch')}</dt>
          <dd>{t(g.kill_switch ? 'common.on' : 'common.off')}</dd>
        </dl>
        {g.scope === 'loopback_only' && <p className="text-sm">{t('publishing.guard.loopbackOnly')}</p>}
      </CardContent>
    </Card>
  )
}
