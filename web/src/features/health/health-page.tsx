import { useTranslation } from 'react-i18next'
import { Link, useParams } from 'react-router'
import { CircleAlertIcon, CircleCheckIcon, CircleMinusIcon } from 'lucide-react'
import { useOrgHealth } from '@/api/hooks/health'
import { Ltr } from '@/components/ltr'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { StatusBadge } from '@/components/status-badge'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { DoctorChecks } from '@/features/models/maintenance-tab'
import { formatDateTime, formatNumber } from '@/lib/format'
import { useLocale } from '@/lib/use-locale'
import { useStartDoctorRun } from '@/api/hooks/catherd'
import { useOperationRunner } from '@/components/use-operation-runner'
import { problemMessage } from '@/lib/problem-message'
import { toast } from 'sonner'

function Ok({ ok, label }: { ok: boolean; label: string }) {
  const Icon = ok ? CircleCheckIcon : CircleAlertIcon
  return (
    <Badge variant={ok ? 'secondary' : 'destructive'}>
      <Icon aria-hidden />
      {label}
    </Badge>
  )
}

export function HealthPage() {
  const { t } = useTranslation()
  const locale = useLocale()
  const { orgId = '' } = useParams()
  const health = useOrgHealth(orgId)
  const runner = useOperationRunner(orgId)
  const doctor = useStartDoctorRun(runner.key)
  return (
    <div className="flex flex-col gap-6">
      <div>
        <h1 className="text-2xl font-semibold">{t('health.title')}</h1>
        <p className="max-w-prose text-sm text-muted-foreground">{t('health.subtitle')}</p>
      </div>
      {health.isPending && <LoadingBlock />}
      {health.isError && <LoadFailed title={t('health.loadFailed')} error={health.error} onRetry={() => void health.refetch()} />}
      {health.data && (
        <div className="grid gap-4 lg:grid-cols-2">
          <Card>
            <CardHeader>
              <CardTitle>{t('health.mongo')}</CardTitle>
            </CardHeader>
            <CardContent className="flex flex-col gap-2 text-sm">
              <Ok ok={health.data.mongo.ok} label={health.data.mongo.ok ? t('health.ok') : t('health.down')} />
              {health.data.mongo.replica_set && (
                <span>
                  {t('health.replicaSet')}: <Ltr>{health.data.mongo.replica_set}</Ltr>
                </span>
              )}
              {!health.data.mongo.replica_set && health.data.mongo.ok && <span className="text-muted-foreground">{t('health.noReplicaSet')}</span>}
            </CardContent>
          </Card>
          <Card>
            <CardHeader>
              <CardTitle>{t('health.runner')}</CardTitle>
            </CardHeader>
            <CardContent className="flex flex-col gap-2 text-sm">
              <Ok ok={health.data.runner.alive} label={health.data.runner.alive ? t('health.alive') : t('health.stopped')} />
              <span className="text-muted-foreground">{t('health.runnerHelp')}</span>
            </CardContent>
          </Card>
          <Card>
            <CardHeader>
              <CardTitle>{t('health.queue')}</CardTitle>
            </CardHeader>
            <CardContent>
              <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-1 text-sm">
                {(['queued', 'leased', 'failed_24h', 'dead_24h'] as const).map((k) => (
                  <div key={k} className="col-span-2 grid grid-cols-subgrid">
                    <dt className="text-muted-foreground">{t(`health.queueFields.${k}`)}</dt>
                    <dd className="tabular-nums">{formatNumber(health.data.queue[k], locale)}</dd>
                  </div>
                ))}
                {health.data.queue.oldest_queued_age_secs != null && (
                  <div className="col-span-2 grid grid-cols-subgrid">
                    <dt className="text-muted-foreground">{t('health.queueFields.oldest')}</dt>
                    <dd className="tabular-nums">{t('health.seconds', { count: formatNumber(health.data.queue.oldest_queued_age_secs, locale) })}</dd>
                  </div>
                )}
              </dl>
            </CardContent>
          </Card>
          <Card>
            <CardHeader>
              <CardTitle>{t('health.connections')}</CardTitle>
            </CardHeader>
            <CardContent>
              {health.data.connections.length === 0 ? (
                <p className="text-sm text-muted-foreground">{t('health.noConnections')}</p>
              ) : (
                <ul className="flex flex-col gap-2 text-sm">
                  {health.data.connections.map((c) => (
                    <li key={c.id} className="flex flex-wrap items-center gap-2">
                      <span className="font-medium">{c.name}</span>
                      <span className="text-muted-foreground">{t(`providers.${c.provider}`)}</span>
                      <StatusBadge status={c.status} />
                      {c.readiness.unattended_ready ? <Badge>{t('readiness.ready')}</Badge> : <Badge variant="outline">{t('readiness.notReady')}</Badge>}
                    </li>
                  ))}
                </ul>
              )}
              <Button asChild variant="link" className="mt-2 px-0">
                <Link to={`/orgs/${orgId}/connections`}>{t('health.openConnections')}</Link>
              </Button>
            </CardContent>
          </Card>
          <Card className="lg:col-span-2">
            <CardHeader className="flex-row flex-wrap items-center justify-between gap-2">
              <CardTitle>{t('health.catherd')}</CardTitle>
              <Button
                variant="outline"
                disabled={!health.data.catherd.available || !health.data.catherd.supported || doctor.isPending || runner.running}
                onClick={() => doctor.mutate({ orgId }, { onSuccess: runner.started, onError: (e) => toast.error(problemMessage(t, e)) })}
              >
                {runner.running ? t('catherd.doctor.running') : t('catherd.doctor.run')}
              </Button>
            </CardHeader>
            <CardContent className="flex flex-col gap-3 text-sm">
              {!health.data.catherd.available && (
                <p role="status" className="flex items-center gap-2" data-degraded>
                  <CircleAlertIcon className="size-4 text-destructive" aria-hidden />
                  {t('health.catherdDown')}
                </p>
              )}
              {health.data.catherd.available && !health.data.catherd.supported && (
                <p role="status" className="flex items-center gap-2" data-degraded>
                  <CircleAlertIcon className="size-4 text-destructive" aria-hidden />
                  {t('health.catherdUnsupported', { version: health.data.catherd.version ?? '?' })}
                </p>
              )}
              {health.data.catherd.available && health.data.catherd.supported && (
                <p className="flex items-center gap-2">
                  <CircleCheckIcon className="size-4" aria-hidden />
                  {t('health.catherdOk')} <Ltr>{health.data.catherd.version}</Ltr>
                </p>
              )}
              {health.data.catherd.last_doctor ? (
                <>
                  <p className="flex flex-wrap items-center gap-2">
                    <Badge variant={health.data.catherd.last_doctor.ready ? 'default' : 'destructive'}>{health.data.catherd.last_doctor.ready ? t('catherd.doctor.ready') : t('catherd.doctor.notReady')}</Badge>
                    <span className="text-muted-foreground">{t('catherd.doctor.at', { time: formatDateTime(health.data.catherd.last_doctor.checked_at, locale) })}</span>
                  </p>
                  <DoctorChecks checks={health.data.catherd.last_doctor.checks} />
                </>
              ) : (
                <p className="flex items-center gap-2 text-muted-foreground">
                  <CircleMinusIcon className="size-4" aria-hidden />
                  {t('catherd.doctor.never')}
                </p>
              )}
              <Button asChild variant="link" className="self-start px-0">
                <Link to={`/orgs/${orgId}/models?tab=maintenance`}>{t('health.openMaintenance')}</Link>
              </Button>
            </CardContent>
          </Card>
        </div>
      )}
    </div>
  )
}
