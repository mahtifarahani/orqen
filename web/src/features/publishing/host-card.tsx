import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useAttestSpike, useHost, useProbeHost } from '@/api/hooks/host'
import { ApiProblem } from '@/api/problem'
import { useOperationRunner } from '@/components/use-operation-runner'
import { Field } from '@/components/field'
import { Ltr } from '@/components/ltr'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { formatDateTime } from '@/lib/format'
import { problemMessage } from '@/lib/problem-message'
import { useLocale } from '@/lib/use-locale'

/** The Codex host: probe it (no thread, no turn) and attest the real spike once the user has run it. */
export function HostCard({ orgId }: { orgId: string }) {
  const { t } = useTranslation()
  const locale = useLocale()
  const host = useHost(orgId)
  const runner = useOperationRunner(orgId)
  const probe = useProbeHost(runner.key)
  const [codex, setCodex] = useState('')
  const [catherd, setCatherd] = useState('')
  const attest = useAttestSpike(orgId, host.data?.revision ?? 0)

  if (host.isPending) return <LoadingBlock />
  if (host.isError) return <LoadFailed title={t('host.loadFailed')} error={host.error} onRetry={() => void host.refetch()} />
  const h = host.data
  const p = h.probe
  const running = probe.isPending || runner.running
  return (
    <Card>
      <CardContent className="flex flex-col gap-3 p-4">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div>
            <h2 className="text-lg font-semibold">{t('host.title')}</h2>
            <p className="text-sm text-muted-foreground">{t('host.subtitle', { version: h.min_codex_version })}</p>
          </div>
          <Badge variant="outline" data-host-available={h.available}>
            {t(h.available ? 'host.available' : 'host.unavailable')}
          </Badge>
        </div>
        {p ? (
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
            <dt className="text-muted-foreground">{t('host.version')}</dt>
            <dd><Ltr>{p.codex_version ?? '-'}</Ltr>{p.fake && <span className="ms-2 text-muted-foreground">{t('host.fake')}</span>}</dd>
            <dt className="text-muted-foreground">{t('host.login')}</dt>
            <dd>{t(`host.loginState.${p.login}`)}</dd>
            <dt className="text-muted-foreground">{t('host.checkedAt')}</dt>
            <dd>{formatDateTime(p.checked_at, locale)}</dd>
          </dl>
        ) : (
          <p className="text-sm text-muted-foreground">{t('host.notProbed')}</p>
        )}
        {p && p.reasons.length > 0 && (
          <Alert variant="destructive">
            <AlertDescription>
              <ul className="list-disc ps-5">
                {p.reasons.map((r) => (
                  <li key={r}>{t(`readiness.blockers.${r}`, { defaultValue: r })}</li>
                ))}
              </ul>
            </AlertDescription>
          </Alert>
        )}
        {probe.isError && (
          <Alert variant="destructive" role="alert">
            <AlertDescription>{problemMessage(t, probe.error)}</AlertDescription>
          </Alert>
        )}
        <div>
          <Button disabled={running} onClick={() => probe.mutate({ orgId }, { onSuccess: runner.started })}>
            {t(running ? 'host.probing' : 'host.probe')}
          </Button>
        </div>
        {p?.status === 'ok' && !p.fake && (
          <form
            className="flex flex-col gap-3 border-t pt-3"
            onSubmit={(e) => {
              e.preventDefault()
              attest.mutate(
                { orgId, data: { codex_version: codex.trim(), catherd_version: catherd.trim(), note: null } },
                { onSuccess: () => toast.success(t('host.attested')) },
              )
            }}
          >
            <h3 className="font-medium">{t('host.spikeTitle')}</h3>
            <p className="text-sm text-muted-foreground">{t('host.spikeHelp')}</p>
            {h.spike ? (
              <p className="text-sm" role="status">
                {t('host.spikeDone', { date: formatDateTime(h.spike.at, locale) })}
              </p>
            ) : (
              <>
                <Field id="spike-codex" label={t('host.spikeCodex')}>
                  {(f) => <Input {...f} dir="ltr" required value={codex} onChange={(e) => setCodex(e.target.value)} />}
                </Field>
                <Field id="spike-catherd" label={t('host.spikeCatherd')}>
                  {(f) => <Input {...f} dir="ltr" required value={catherd} onChange={(e) => setCatherd(e.target.value)} />}
                </Field>
                {attest.isError && !(attest.error instanceof ApiProblem && attest.error.code === 'revision_conflict') && (
                  <Alert variant="destructive" role="alert">
                    <AlertDescription>{problemMessage(t, attest.error)}</AlertDescription>
                  </Alert>
                )}
                <div>
                  <Button type="submit" variant="outline" disabled={attest.isPending || !codex.trim() || !catherd.trim()}>
                    {t('host.attest')}
                  </Button>
                </div>
              </>
            )}
          </form>
        )}
      </CardContent>
    </Card>
  )
}
