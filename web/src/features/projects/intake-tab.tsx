import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import type { ProjectDto } from '@/api/generated/model'
import { useProjectIntake, usePollNow } from '@/api/hooks/tasks'
import { Ltr } from '@/components/ltr'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { useOperationRunner } from '@/components/use-operation-runner'
import { formatDateTime, formatNumber } from '@/lib/format'
import { problemMessage } from '@/lib/problem-message'
import { useLocale } from '@/lib/use-locale'

export function IntakeTab({ orgId, project }: { orgId: string; project: ProjectDto }) {
  const { t } = useTranslation()
  const locale = useLocale()
  const intake = useProjectIntake(orgId, project.id)
  const runner = useOperationRunner(orgId)
  const poll = usePollNow(runner.key)
  if (intake.isPending) return <LoadingBlock />
  if (intake.isError) return <LoadFailed title={t('intake.loadFailed')} error={intake.error} onRetry={() => void intake.refetch()} />
  const s = intake.data
  const disabled = !project.mapping?.jira || s.paused || poll.isPending || runner.running
  return (
    <div className="flex max-w-3xl flex-col gap-4">
      {s.paused && (
        <Alert>
          <AlertTitle>{t('automation.paused')}</AlertTitle>
          <AlertDescription>{t('automation.help')}</AlertDescription>
        </Alert>
      )}
      {!s.enabled && <p className="text-sm text-muted-foreground">{t('intake.notEnabled')}</p>}
      {s.blocked && (
        <Alert variant="destructive" role="alert">
          <AlertTitle>{t('intake.blocked.title')}</AlertTitle>
          <AlertDescription>{t('intake.blocked.body')}</AlertDescription>
        </Alert>
      )}
      {s.waiting_network && (
        <Alert>
          <AlertTitle>{t('intake.waitingNetwork')}</AlertTitle>
        </Alert>
      )}
      {s.last_error && !s.blocked && (
        <Alert variant="destructive">
          <AlertTitle>{t(`intake.error.${s.last_error.code}`, { defaultValue: s.last_error.code })}</AlertTitle>
          <AlertDescription>{formatDateTime(s.last_error.at, locale)}</AlertDescription>
        </Alert>
      )}
      <Card>
        <CardHeader>
          <CardTitle>{t('intake.title')}</CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col gap-2 text-sm">
          <p>
            {t('intake.mode')}: <Badge variant="outline">{t(`intake.modes.${s.next_mode}`)}</Badge>
          </p>
          <p>
            {t('intake.lastPoll')}: {s.last_success_at ? formatDateTime(s.last_success_at, locale) : t('common.never')}
          </p>
          <p>
            {t('intake.nextPoll')}: {s.next_poll_at ? formatDateTime(s.next_poll_at, locale) : '—'}
          </p>
          {s.last_counts && (
            <p className="text-muted-foreground">
              {t('intake.counts', { seen: formatNumber(s.last_counts.seen, locale), changed: formatNumber(s.last_counts.changed, locale), left: formatNumber(s.last_counts.left_filter, locale) })}
            </p>
          )}
          <p>
            {t('intake.sprints')}: {s.active_sprints.length === 0 ? t('intake.noSprints') : s.active_sprints.map((id) => <Ltr key={id} className="me-2">{id}</Ltr>)}
          </p>
          <div>
            <Button
              disabled={disabled}
              onClick={() =>
                poll.mutate({ orgId, prjId: project.id }, { onSuccess: runner.started, onError: (e) => toast.error(problemMessage(t, e)) })
              }
            >
              {runner.running ? t('intake.polling') : t('intake.pollNow')}
            </Button>
          </div>
        </CardContent>
      </Card>
    </div>
  )
}
