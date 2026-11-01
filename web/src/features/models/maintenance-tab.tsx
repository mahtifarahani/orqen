import { useTranslation } from 'react-i18next'
import { CircleAlertIcon, CircleCheckIcon, CircleMinusIcon, InfoIcon, TriangleAlertIcon } from 'lucide-react'
import { toast } from 'sonner'
import type { DoctorCheck } from '@/api/generated/model'
import { useCatherdStatus, useStartDoctorRun } from '@/api/hooks/catherd'
import { Ltr } from '@/components/ltr'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { useOperationRunner } from '@/components/use-operation-runner'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { formatDateTime } from '@/lib/format'
import { problemMessage } from '@/lib/problem-message'
import { useLocale } from '@/lib/use-locale'

const STATE_ICON = { ok: CircleCheckIcon, warn: TriangleAlertIcon, fail: CircleAlertIcon, skip: CircleMinusIcon, info: InfoIcon } as const

export function DoctorChecks({ checks }: { checks: DoctorCheck[] }) {
  const { t } = useTranslation()
  return (
    <ul className="flex flex-col divide-y rounded-lg border" aria-label={t('catherd.doctor.checks')}>
      {checks.map((c) => {
        const Icon = STATE_ICON[c.state] ?? InfoIcon
        return (
          <li key={c.id} className="flex flex-col gap-1 p-3 text-sm" data-state={c.state}>
            <span className="flex flex-wrap items-center gap-2">
              <Icon className="size-4" aria-hidden />
              <Badge variant={c.state === 'fail' ? 'destructive' : 'outline'}>{t(`catherd.doctor.states.${c.state}`)}</Badge>
              <span className="font-medium">{c.label}</span>
            </span>
            {c.detail && <span className="text-muted-foreground">{c.detail}</span>}
            {c.fix && (
              <span className="text-xs">
                {t('catherd.doctor.fix')}: <Ltr className="font-sans">{c.fix}</Ltr>
              </span>
            )}
          </li>
        )
      })}
    </ul>
  )
}

/** catherd status, the explicit "Run doctor" action and the last doctor report. */
export function MaintenanceTab({ orgId }: { orgId: string }) {
  const { t } = useTranslation()
  const locale = useLocale()
  const status = useCatherdStatus(orgId)
  const runner = useOperationRunner(orgId)
  const doctor = useStartDoctorRun(runner.key)
  if (status.isPending) return <LoadingBlock />
  if (status.isError) return <LoadFailed title={t('catherd.loadFailed')} error={status.error} onRetry={() => void status.refetch()} />
  const s = status.data
  const last = s.last_doctor
  return (
    <div className="flex flex-col gap-4">
      {!s.available && (
        <Alert variant="destructive">
          <AlertTitle>{t('catherd.unavailable')}</AlertTitle>
          <AlertDescription>{t('catherd.unavailableHelp')}</AlertDescription>
        </Alert>
      )}
      {s.available && !s.supported && (
        <Alert variant="destructive">
          <AlertTitle>{t('catherd.unsupported')}</AlertTitle>
          <AlertDescription>{t('catherd.unsupportedHelp')}</AlertDescription>
        </Alert>
      )}
      <Card>
        <CardHeader>
          <CardTitle>{t('catherd.status')}</CardTitle>
        </CardHeader>
        <CardContent>
          <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-1 text-sm">
            <dt className="text-muted-foreground">{t('catherd.version')}</dt>
            <dd className="flex items-center gap-2">
              {s.version ? <Ltr>{s.version}</Ltr> : t('common.unknown')}
              <Badge variant={s.supported ? 'default' : 'destructive'}>{s.supported ? t('catherd.supported') : t('catherd.notSupported')}</Badge>
            </dd>
            <dt className="text-muted-foreground">{t('catherd.binary')}</dt>
            <dd>{s.bin ? <Ltr className="break-all">{s.bin}</Ltr> : t('common.none')}</dd>
            <dt className="text-muted-foreground">{t('catherd.home')}</dt>
            <dd>
              <Ltr className="break-all">{s.home_display}</Ltr>
            </dd>
            {s.path_source && (
              <>
                <dt className="text-muted-foreground">{t('catherd.pathSource')}</dt>
                <dd>
                  <Ltr>{s.path_source}</Ltr>
                </dd>
              </>
            )}
            {s.catalog && (
              <>
                <dt className="text-muted-foreground">{t('catherd.catalog')}</dt>
                <dd>{t('catherd.catalogSummary', { count: s.catalog.model_count, time: formatDateTime(s.catalog.refreshed_at, locale) })}</dd>
              </>
            )}
            {s.inventory && (
              <>
                <dt className="text-muted-foreground">{t('catherd.inventory')}</dt>
                <dd className="flex flex-wrap gap-1">
                  {s.inventory.names.length === 0 ? t('common.none') : s.inventory.names.map((n) => <Badge key={n} variant="outline"><Ltr>{n}</Ltr></Badge>)}
                </dd>
              </>
            )}
          </dl>
        </CardContent>
      </Card>
      <Card>
        <CardHeader className="flex-row flex-wrap items-center justify-between gap-2">
          <CardTitle>{t('catherd.doctor.title')}</CardTitle>
          <Button
            variant="outline"
            disabled={!s.available || !s.supported || doctor.isPending || runner.running}
            onClick={() => doctor.mutate({ orgId }, { onSuccess: runner.started, onError: (e) => toast.error(problemMessage(t, e)) })}
          >
            {runner.running ? t('catherd.doctor.running') : t('catherd.doctor.run')}
          </Button>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          <p className="max-w-prose text-sm text-muted-foreground">{t('catherd.doctor.help')}</p>
          {runner.failed && <Alert variant="destructive"><AlertDescription>{t('catherd.doctor.failed')}</AlertDescription></Alert>}
          {!last && <p className="text-sm text-muted-foreground">{t('catherd.doctor.never')}</p>}
          {last && (
            <>
              <div className="flex flex-wrap items-center gap-2 text-sm">
                <Badge variant={last.ready ? 'default' : 'destructive'}>{last.ready ? t('catherd.doctor.ready') : t('catherd.doctor.notReady')}</Badge>
                <span className="text-muted-foreground">{t('catherd.doctor.at', { time: formatDateTime(last.checked_at, locale) })}</span>
              </div>
              <DoctorChecks checks={last.checks} />
            </>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
