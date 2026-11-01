import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { CircleAlertIcon, PauseIcon, PlayIcon, TriangleAlertIcon } from 'lucide-react'
import { toast } from 'sonner'
import type { ProjectDto } from '@/api/generated/model'
import { useDisableAutomation, useEnableAutomation, useProjectReadiness } from '@/api/hooks/projects'
import { newIdempotencyKey } from '@/api/idempotency'
import { ApiProblem } from '@/api/problem'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { formatDateTime } from '@/lib/format'
import { problemMessage } from '@/lib/problem-message'
import { readinessText } from '@/lib/readiness-text'
import { useLocale } from '@/lib/use-locale'

export function ReadinessTab({ orgId, project, goTab }: { orgId: string; project: ProjectDto; goTab: (tab: string) => void }) {
  const { t } = useTranslation()
  const locale = useLocale()
  const readiness = useProjectReadiness(orgId, project.id)
  const [key, setKey] = useState(newIdempotencyKey)
  const enable = useEnableAutomation(orgId, project.revision, key)
  const disable = useDisableAutomation(orgId, project.revision)
  const [refused, setRefused] = useState<{ field: string; message: string }[]>([])

  const doEnable = () => {
    setRefused([])
    enable.mutate(
      { orgId, prjId: project.id },
      {
        onSuccess: () => {
          setKey(newIdempotencyKey())
          toast.success(t('readiness.enabled'))
        },
        onError: (err) => {
          if (err instanceof ApiProblem && err.code === 'project_not_ready') {
            const list = err.errors ?? []
            setRefused(list)
            toast.error(t('problems.project_not_ready'), {
              description: list.map((e) => readinessText(t, 'blockers', e.field, e.message)).join(' · '),
            })
          } else {
            toast.error(problemMessage(t, err))
          }
        },
      },
    )
  }
  const doDisable = () =>
    disable.mutate({ orgId, prjId: project.id }, { onSuccess: () => toast.success(t('readiness.disabled')), onError: (e) => toast.error(problemMessage(t, e)) })

  if (readiness.isPending) return <LoadingBlock />
  if (readiness.isError) return <LoadFailed title={t('readiness.loadFailed')} error={readiness.error} onRetry={() => void readiness.refetch()} />
  const r = readiness.data
  const reasons = r.blockers.map((b) => readinessText(t, 'blockers', b.code, b.detail))

  return (
    <div className="flex flex-col gap-4">
      <Card>
        <CardHeader>
          <CardTitle>{r.ready ? t('readiness.ready') : t('readiness.notReady')}</CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col gap-4">
          {project.automation_enabled && r.blockers.length > 0 && (
            <Alert variant="destructive">
              <TriangleAlertIcon />
              <AlertTitle>{t('readiness.enabledBlocked')}</AlertTitle>
              <AlertDescription>{t('readiness.enabledBlockedBody')}</AlertDescription>
            </Alert>
          )}
          {r.blockers.length === 0 ? (
            <p className="text-sm text-muted-foreground">{t('projects.detail.noBlockers')}</p>
          ) : (
            <ul className="flex flex-col divide-y rounded-lg border" aria-label={t('projects.detail.blockers')}>
              {r.blockers.map((b) => (
                <li key={b.code} className="flex flex-wrap items-center justify-between gap-2 p-3 text-sm">
                  <span className="flex items-start gap-2">
                    <CircleAlertIcon className="mt-0.5 size-4 shrink-0 text-destructive" aria-hidden />
                    <span>{readinessText(t, 'blockers', b.code, b.detail)}</span>
                  </span>
                  <Button size="sm" variant="outline" onClick={() => goTab(b.tab)}>
                    {t('readiness.fix')}
                    <span className="sr-only"> — {t(`projects.tabs.${b.tab}`)}</span>
                  </Button>
                </li>
              ))}
            </ul>
          )}
          {r.warnings.length > 0 && (
            <div>
              <h3 className="mb-1 text-sm font-medium">{t('projects.detail.warnings')}</h3>
              <ul className="flex list-disc flex-col gap-1 ps-5 text-sm text-muted-foreground">
                {r.warnings.map((w) => (
                  <li key={w.code}>{readinessText(t, 'warnings', w.code, w.detail)}</li>
                ))}
              </ul>
            </div>
          )}
          {refused.length > 0 && (
            <Alert variant="destructive" role="alert">
              <AlertTitle>{t('problems.project_not_ready')}</AlertTitle>
              <AlertDescription>
                <ul className="list-disc ps-5">
                  {refused.map((e) => (
                    <li key={e.field}>{readinessText(t, 'blockers', e.field, e.message)}</li>
                  ))}
                </ul>
              </AlertDescription>
            </Alert>
          )}
          <div className="flex flex-wrap items-center gap-3">
            {project.automation_enabled ? (
              <Button variant="outline" onClick={doDisable} disabled={disable.isPending}>
                <PauseIcon />
                {t('readiness.disable')}
              </Button>
            ) : (
              <Button onClick={doEnable} disabled={!r.ready || enable.isPending} aria-describedby={!r.ready ? 'enable-reasons' : undefined}>
                <PlayIcon />
                {t('readiness.enable')}
              </Button>
            )}
            {!r.ready && !project.automation_enabled && (
              <p id="enable-reasons" className="max-w-prose text-xs text-muted-foreground">
                {t('readiness.enableDisabled', { reasons: reasons.join(' · ') })}
              </p>
            )}
            <span className="text-xs text-muted-foreground">{t('readiness.evaluatedAt', { time: formatDateTime(r.evaluated_at, locale) })}</span>
          </div>
        </CardContent>
      </Card>
    </div>
  )
}
