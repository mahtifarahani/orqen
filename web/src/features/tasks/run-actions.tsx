import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { BanIcon, PlayIcon, RotateCcwIcon, StepForwardIcon } from 'lucide-react'
import { toast } from 'sonner'
import type { RunDto } from '@/api/generated/model'
import { useCancelRun, useResumeRun, useRetryRun, useStartRun, type TaskSummary } from '@/api/hooks/tasks'
import { ApiProblem } from '@/api/problem'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Textarea } from '@/components/ui/textarea'
import { Label } from '@/components/ui/label'
import { problemMessage } from '@/lib/problem-message'
import { readinessText } from '@/lib/readiness-text'
import { useIntentKey } from '@/lib/use-intent-key'
import { EvaluateButton } from './tasks-page'

const RESUMABLE_HOLDS = ['project_not_ready', 'project_not_enabled', 'automation_paused', 'left_filter']
const STARTABLE_HOLDS = ['coding_start', 'project_not_ready', 'project_not_enabled']

/** Evaluate, Start, Resume, Retry and Cancel for a task's latest run. Every click sends If-Match and one Idempotency-Key. */
export function RunActions({ orgId, task, run }: { orgId: string; task: Pick<TaskSummary, 'id' | 'issue_key' | 'candidate'>; run: RunDto | undefined }) {
  const { t } = useTranslation()
  const [blockers, setBlockers] = useState<{ field: string; message: string }[]>([])
  const rev = run?.revision ?? 0
  const [startKey, renewStart] = useIntentKey()
  const [resumeKey, renewResume] = useIntentKey()
  const [retryKey, renewRetry] = useIntentKey()
  const [cancelKey, renewCancel] = useIntentKey()
  const start = useStartRun(orgId, { revision: rev, idempotencyKey: startKey })
  const resume = useResumeRun(orgId, { revision: rev, idempotencyKey: resumeKey })
  const retry = useRetryRun(orgId, { revision: rev, idempotencyKey: retryKey })
  const cancel = useCancelRun(orgId, { revision: rev, idempotencyKey: cancelKey })
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [reason, setReason] = useState('')

  const fail = (e: unknown) => toast.error(problemMessage(t, e))
  if (!run) return task.candidate ? <EvaluateButton orgId={orgId} task={task} size="default" /> : null
  const canStart = run.active && run.state === 'planned' && !!run.hold && STARTABLE_HOLDS.includes(run.hold.code)
  const canResume = run.active && ((!!run.hold && RESUMABLE_HOLDS.includes(run.hold.code)) || run.state === 'waiting_network')
  const canRetry = !run.active && (run.state === 'failed' || run.state === 'cancelled')
  const canCancel = run.active

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        {task.candidate && !run.active && <EvaluateButton orgId={orgId} task={task} size="default" />}
        {canStart && (
          <Button
            disabled={start.isPending || blockers.length > 0}
            aria-describedby={blockers.length > 0 ? 'start-blockers' : undefined}
            onClick={() => {
              setBlockers([])
              start.mutate(
                { orgId, runId: run.id },
                {
                  onSuccess: () => {
                    renewStart()
                    toast.success(t('run.started'))
                  },
                  onError: (e) => {
                    if (e instanceof ApiProblem && e.code === 'run_not_startable') setBlockers(e.errors ?? [])
                    else fail(e)
                  },
                },
              )
            }}
          >
            <PlayIcon />
            {t('run.actions.start')}
          </Button>
        )}
        {canResume && (
          <Button
            variant="outline"
            disabled={resume.isPending}
            onClick={() =>
              resume.mutate({ orgId, runId: run.id }, { onSuccess: () => { renewResume(); toast.success(t('run.resumed')) }, onError: fail })
            }
          >
            <StepForwardIcon />
            {t('run.actions.resume')}
          </Button>
        )}
        {canRetry && (
          <Button
            variant="outline"
            disabled={retry.isPending}
            onClick={() => retry.mutate({ orgId, runId: run.id }, { onSuccess: () => { renewRetry(); toast.success(t('run.retried')) }, onError: fail })}
          >
            <RotateCcwIcon />
            {t('run.actions.retry')}
          </Button>
        )}
        {canCancel && (
          <Button variant="outline" onClick={() => setConfirmOpen(true)}>
            <BanIcon />
            {t('run.actions.cancel')}
          </Button>
        )}
      </div>
      {blockers.length > 0 && (
        <Alert variant="destructive" id="start-blockers" role="alert" data-testid="start-blockers">
          <AlertTitle>{t('run.startBlocked')}</AlertTitle>
          <AlertDescription>
            <ul className="list-disc ps-5">
              {blockers.map((b) => (
                <li key={b.field}>{readinessText(t, 'blockers', b.field, b.message)}</li>
              ))}
            </ul>
            <Button size="sm" variant="outline" className="mt-2" onClick={() => setBlockers([])}>
              {t('run.checkAgain')}
            </Button>
          </AlertDescription>
        </Alert>
      )}
      <Dialog open={confirmOpen} onOpenChange={setConfirmOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('run.cancelDialog.title')}</DialogTitle>
            <DialogDescription>{t('run.cancelDialog.body')}</DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-2">
            <Label htmlFor="cancel-reason">{t('run.cancelDialog.reason')}</Label>
            <Textarea id="cancel-reason" dir="auto" maxLength={500} value={reason} onChange={(e) => setReason(e.target.value)} />
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirmOpen(false)}>
              {t('run.cancelDialog.keep')}
            </Button>
            <Button
              variant="destructive"
              disabled={cancel.isPending}
              onClick={() =>
                cancel.mutate(
                  { orgId, runId: run.id, data: reason.trim() ? { reason: reason.trim() } : {} },
                  { onSuccess: () => { renewCancel(); setConfirmOpen(false); setReason(''); toast.success(t('run.cancelled')) }, onError: fail },
                )
              }
            >
              {t('run.cancelDialog.confirm')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
