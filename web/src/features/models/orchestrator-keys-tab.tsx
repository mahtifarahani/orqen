import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router'
import { toast } from 'sonner'
import type { CatherdSecretDto, OrchestratorHost, OrchestratorHostDto } from '@/api/generated/model'
import { useCatherdSecrets, useCatherdStatus, useCheckCatherdSecretRun, useInvalidateKeys, useOrchestratorHosts, useRemoveCatherdSecret, useStartDoctorRun } from '@/api/hooks/catherd'
import { useOrgPolicyEffective } from '@/api/hooks/policy'
import { Ltr } from '@/components/ltr'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { useOperationRunner } from '@/components/use-operation-runner'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { formatDateTime } from '@/lib/format'
import { problemMessage } from '@/lib/problem-message'
import { useLocale } from '@/lib/use-locale'
import { SecretDialog } from './secret-dialog'

export const HOST_POLICY_KEY = 'orchestrator.host'

/** The "Orchestrator & keys" tab: which host catherd plans for, and the keys Orqen passes to catherd. */
export function OrchestratorKeysTab({ orgId }: { orgId: string }) {
  return (
    <div className="flex flex-col gap-6">
      <HostCard orgId={orgId} />
      <KeysCard orgId={orgId} />
    </div>
  )
}

/**
 * The host is an organization policy key (versioned and audited by the policy flow). Choosing another host does not
 * change anything here: it opens the policy draft with the value filled in, and publishing happens there.
 */
export function HostCard({ orgId }: { orgId: string }) {
  const { t } = useTranslation()
  const locale = useLocale()
  const navigate = useNavigate()
  const hosts = useOrchestratorHosts(orgId)
  const status = useCatherdStatus(orgId)
  const effective = useOrgPolicyEffective(orgId)
  const runner = useOperationRunner(orgId)
  const doctor = useStartDoctorRun(runner.key)
  const [pending, setPending] = useState<OrchestratorHost | null>(null)
  if (hosts.isPending) return <LoadingBlock />
  if (hosts.isError) return <LoadFailed title={t('models.orchestrator.loadFailed')} error={hosts.error} onRetry={() => void hosts.refetch()} />
  const entry = effective.data?.items.find((i) => i.key === HOST_POLICY_KEY)
  const current = hosts.data.find((h) => h.effective)?.host
  const version = status.data?.version

  const openDraft = (host: OrchestratorHost) => {
    setPending(null)
    navigate(`/orgs/${orgId}/settings?tab=policy&prefill=${encodeURIComponent(HOST_POLICY_KEY)}&value=${encodeURIComponent(host)}`)
  }
  const runDoctor = (host: OrchestratorHost) => doctor.mutate({ orgId, data: { host } }, { onSuccess: runner.started, onError: (e) => toast.error(problemMessage(t, e)) })

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('models.orchestrator.title')}</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <p className="max-w-prose text-sm text-muted-foreground">{t('models.orchestrator.hostHelp')}</p>
        <div className="flex flex-wrap items-center gap-2 text-sm">
          {entry && (
            <Badge variant="outline" data-source={entry.source.layer}>
              {t(`policy.source.${entry.source.layer}`)}
              {entry.source.layer !== 'product' && <span className="tabular-nums"> v{entry.source.number}</span>}
            </Badge>
          )}
          {version && (
            <span className="text-muted-foreground">
              catherd <Ltr>{version}</Ltr>
            </span>
          )}
        </div>
        {runner.failed && (
          <Alert variant="destructive" role="alert">
            <AlertDescription>{t('catherd.doctor.failed')}</AlertDescription>
          </Alert>
        )}
        <div role="radiogroup" aria-label={t('models.orchestrator.host')} className="flex flex-col gap-3">
          {hosts.data.map((h) => (
            <HostOption key={h.host} host={h} checked={h.host === current} busy={doctor.isPending || runner.running} locale={locale} onChoose={() => setPending(h.host)} onDoctor={() => runDoctor(h.host)} />
          ))}
        </div>
        <p className="max-w-prose text-xs text-muted-foreground">{t('models.orchestrator.republishHint')}</p>
      </CardContent>
      <Dialog open={pending !== null} onOpenChange={(o) => !o && setPending(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('models.orchestrator.changeTitle', { host: pending ?? '' })}</DialogTitle>
            <DialogDescription>{t('models.orchestrator.changeBody')}</DialogDescription>
          </DialogHeader>
          {pending && hosts.data.find((h) => h.host === pending)?.orqen_host_adapter === 'unavailable' && (
            <Alert variant="destructive">
              <AlertDescription>{t('models.orchestrator.claudeHostUnavailable')}</AlertDescription>
            </Alert>
          )}
          <p className="text-sm text-muted-foreground">{t('models.orchestrator.republishHint')}</p>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setPending(null)}>
              {t('common.cancel')}
            </Button>
            <Button type="button" onClick={() => pending && openDraft(pending)}>
              {t('models.orchestrator.openDraft')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  )
}

function HostOption({ host: h, checked, busy, locale, onChoose, onDoctor }: { host: OrchestratorHostDto; checked: boolean; busy: boolean; locale: string; onChoose: () => void; onDoctor: () => void }) {
  const { t } = useTranslation()
  const id = `orch-host-${h.host}`
  const d = h.last_doctor
  return (
    <div className="flex flex-col gap-2 rounded-lg border p-3" data-host={h.host}>
      <div className="flex flex-wrap items-center gap-2">
        <input type="radio" id={id} name="orchestrator-host" className="size-4" checked={checked} onChange={onChoose} />
        <label htmlFor={id} className="flex items-center gap-2 text-sm font-medium">
          <Ltr className="font-sans">{h.host}</Ltr>
        </label>
        {h.effective && <Badge>{t('models.orchestrator.effective')}</Badge>}
        {h.orqen_host_adapter === 'unavailable' && <Badge variant="destructive">{t('models.orchestrator.adapterUnavailable')}</Badge>}
      </div>
      {h.orqen_host_adapter === 'unavailable' && <p className="text-xs text-muted-foreground">{t('models.orchestrator.claudeHostUnavailable')}</p>}
      <div className="flex flex-wrap items-center gap-2 text-sm">
        {d ? (
          <>
            <Badge variant={d.ready ? 'default' : 'destructive'}>{d.ready ? t('catherd.doctor.ready') : t('catherd.doctor.notReady')}</Badge>
            <span className="text-muted-foreground">{t('catherd.doctor.at', { time: formatDateTime(d.checked_at, locale as never) })}</span>
          </>
        ) : (
          <span className="text-muted-foreground">{t('catherd.doctor.never')}</span>
        )}
        <Button type="button" size="sm" variant="outline" disabled={busy} onClick={onDoctor}>
          {t('models.orchestrator.runDoctor')}
          <span className="sr-only"> ({h.host})</span>
        </Button>
      </div>
    </div>
  )
}

/** The keys table: one row per kind, write-only values, explicit check, confirmed removal. */
export function KeysCard({ orgId }: { orgId: string }) {
  const { t } = useTranslation()
  const secrets = useCatherdSecrets(orgId)
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('models.keys.title')}</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <p className="max-w-prose text-sm text-muted-foreground">{t('models.keys.note')}</p>
        {secrets.isPending && <LoadingBlock />}
        {secrets.isError && <LoadFailed title={t('models.keys.loadFailed')} error={secrets.error} onRetry={() => void secrets.refetch()} />}
        {secrets.isSuccess && (
          <div className="overflow-x-auto rounded-lg border">
            <Table aria-label={t('models.keys.title')}>
              <TableHeader>
                <TableRow>
                  <TableHead>{t('models.keys.columns.key')}</TableHead>
                  <TableHead>{t('models.keys.columns.env')}</TableHead>
                  <TableHead>{t('models.keys.columns.usedBy')}</TableHead>
                  <TableHead>{t('models.keys.columns.state')}</TableHead>
                  <TableHead>{t('models.keys.columns.actions')}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {secrets.data.map((s) => (
                  <KeyRow key={s.kind} orgId={orgId} secret={s} />
                ))}
              </TableBody>
            </Table>
          </div>
        )}
      </CardContent>
    </Card>
  )
}

function KeyState({ secret: s }: { secret: CatherdSecretDto }) {
  const { t } = useTranslation()
  const locale = useLocale()
  if (s.state === 'none') return <Badge variant="outline">{t('models.keys.state.none')}</Badge>
  const c = s.last_check
  if (!c) {
    return (
      <Badge variant="secondary">
        {t('models.keys.state.set')}
        {s.updated_at && <span> · {formatDateTime(s.updated_at, locale)}</span>}
      </Badge>
    )
  }
  const variant = c.status === 'ok' ? 'default' : c.status === 'refused' ? 'destructive' : 'outline'
  return (
    <span className="flex flex-col gap-1">
      <Badge variant={variant} data-check={c.status}>
        {t(`models.keys.state.${c.status === 'ok' ? 'ok' : c.status}`)}
      </Badge>
      <span className="text-xs text-muted-foreground">{t('models.keys.checkedAt', { time: formatDateTime(c.checked_at, locale) })}</span>
    </span>
  )
}

function KeyRow({ orgId, secret: s }: { orgId: string; secret: CatherdSecretDto }) {
  const { t } = useTranslation()
  const [editing, setEditing] = useState(false)
  const [removing, setRemoving] = useState(false)
  const refresh = useInvalidateKeys(orgId)
  const runner = useOperationRunner(orgId)
  const check = useCheckCatherdSecretRun(runner.key)
  const remove = useRemoveCatherdSecret(orgId, s.revision)
  const isSet = s.state === 'set'
  // The finished check changed this row; reload without waiting for the event stream.
  useEffect(() => {
    if (runner.succeeded) refresh()
    // eslint-disable-next-line react-hooks/exhaustive-deps -- reacts to the operation outcome only
  }, [runner.succeeded])
  const name = t(`secrets.kind.${s.kind}`)
  return (
    <TableRow data-kind={s.kind}>
      <TableCell className="whitespace-normal font-medium">
        {name}
        {!s.verifiable && <div className="text-xs font-normal text-muted-foreground">{t('models.keys.cannotVerifyHint')}</div>}
        {s.required_reason && <div className="text-xs font-normal text-muted-foreground">{t('models.keys.requiredFor')}</div>}
      </TableCell>
      <TableCell>
        <Ltr className="text-xs">{s.env_name}</Ltr>
      </TableCell>
      <TableCell className="whitespace-normal text-sm text-muted-foreground">{s.used_by.map((u) => t(`models.keys.usedBy.${u}`, { defaultValue: u })).join(', ')}</TableCell>
      <TableCell className="whitespace-normal">
        <KeyState secret={s} />
      </TableCell>
      <TableCell>
        <div className="flex flex-wrap gap-2">
          <Button size="sm" variant="outline" onClick={() => setEditing(true)}>
            {isSet ? t('models.keys.actions.replace') : t('models.keys.actions.set')}
            <span className="sr-only"> — {name}</span>
          </Button>
          <Button
            size="sm"
            variant="outline"
            disabled={!isSet || check.isPending || runner.running}
            onClick={() => check.mutate({ orgId, kind: s.kind }, { onSuccess: runner.started, onError: (e) => toast.error(problemMessage(t, e)) })}
          >
            {runner.running ? t('models.keys.checking') : t('models.keys.actions.check')}
            <span className="sr-only"> — {name}</span>
          </Button>
          <Button size="sm" variant="ghost" disabled={!isSet} onClick={() => setRemoving(true)}>
            {t('models.keys.actions.remove')}
            <span className="sr-only"> — {name}</span>
          </Button>
        </div>
        {runner.failed && <p className="mt-1 text-xs text-destructive">{t('models.keys.checkFailed')}</p>}
      </TableCell>
      {editing && <SecretDialog orgId={orgId} secret={s} open onOpenChange={setEditing} />}
      <Dialog open={removing} onOpenChange={setRemoving}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('models.keys.dialog.removeTitle', { name })}</DialogTitle>
            <DialogDescription>{t('models.keys.dialog.removeBody')}</DialogDescription>
          </DialogHeader>
          {remove.isError && (
            <Alert variant="destructive" role="alert">
              <AlertDescription>{problemMessage(t, remove.error)}</AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setRemoving(false)}>
              {t('common.cancel')}
            </Button>
            <Button
              type="button"
              variant="destructive"
              disabled={remove.isPending}
              onClick={() =>
                remove.mutate(
                  { orgId, kind: s.kind },
                  {
                    onSuccess: () => {
                      toast.success(t('models.keys.dialog.removed'))
                      setRemoving(false)
                    },
                  },
                )
              }
            >
              {t('models.keys.dialog.confirmRemove')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </TableRow>
  )
}
