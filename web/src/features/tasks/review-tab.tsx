import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import type { ReviewFindingDto, RunDto } from '@/api/generated/model'
import { useRunFindings, useWaiveFindingMutation } from '@/api/hooks/runDetail'
import { ApiProblem } from '@/api/problem'
import { Field } from '@/components/field'
import { Ltr } from '@/components/ltr'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Textarea } from '@/components/ui/textarea'
import { problemMessage } from '@/lib/problem-message'
import { useIntentKey } from '@/lib/use-intent-key'

/** The independent review of the current round: verdict, independence, findings, and waiving a blocking one. */
export function ReviewTab({ orgId, run }: { orgId: string; run: RunDto | undefined }) {
  const { t } = useTranslation()
  const findings = useRunFindings(orgId, run?.id)
  const [waiving, setWaiving] = useState<ReviewFindingDto | null>(null)
  if (!run) return <p className="text-sm text-muted-foreground">{t('taskDetail.noRun')}</p>
  const r = run.review
  if (!r) return <p className="text-sm text-muted-foreground">{t('review.none')}</p>
  return (
    <div className="flex flex-col gap-4">
      <Card>
        <CardContent className="flex flex-wrap items-center gap-3 p-4 text-sm">
          <Badge variant="outline" data-verdict={r.verdict}>
            {t(`review.verdict.${r.verdict}`, { defaultValue: r.verdict })}
          </Badge>
          <span>{t('review.round', { n: r.round })}</span>
          <span>{t('review.blockingOpen', { n: r.blocking_open })}</span>
          <Badge variant="outline" data-independence={r.independence.status}>
            {t(`review.independence.${r.independence.status}`, { defaultValue: r.independence.status })}
          </Badge>
          <Ltr className="font-mono text-xs text-muted-foreground">{r.head.slice(0, 12)}</Ltr>
        </CardContent>
      </Card>
      {findings.isPending && <LoadingBlock rows={2} />}
      {findings.isError && <LoadFailed title={t('review.loadFailed')} error={findings.error} onRetry={() => void findings.refetch()} />}
      {findings.data && findings.data.length === 0 && <p className="text-sm text-muted-foreground">{t('review.noFindings')}</p>}
      <ul className="flex flex-col gap-3" aria-label={t('review.findings')}>
        {findings.data?.map((f) => (
          <li key={f.id} className="rounded-lg border p-3 text-sm" data-finding-status={f.status}>
            <div className="flex flex-wrap items-center gap-2">
              <Badge variant={f.blocking ? 'destructive' : 'outline'}>{t(`review.severity.${f.severity}`)}</Badge>
              <Badge variant="outline">{t(`review.status.${f.status}`)}</Badge>
              {!f.inline_eligible && <Badge variant="outline">{t('review.summaryOnly')}</Badge>}
              <span className="font-medium" dir="auto">
                {f.title}
              </span>
            </div>
            <p className="mt-1 text-muted-foreground">
              <Ltr>
                {f.file}
                {f.line > 0 ? `:${f.line}` : ''}
              </Ltr>
            </p>
            <p className="mt-1 whitespace-pre-wrap" dir="auto">
              {f.body}
            </p>
            {f.waiver && (
              <p className="mt-1" role="status">
                {t('review.waivedBecause')}: <span dir="auto">{f.waiver.text}</span>
              </p>
            )}
            {f.status === 'open' && f.blocking && (
              <Button className="mt-2" size="sm" variant="outline" onClick={() => setWaiving(f)}>
                {t('review.waive')}
              </Button>
            )}
          </li>
        ))}
      </ul>
      {waiving && <WaiveDialog orgId={orgId} run={run} finding={waiving} onClose={() => setWaiving(null)} />}
    </div>
  )
}

function WaiveDialog({ orgId, run, finding, onClose }: { orgId: string; run: RunDto; finding: ReviewFindingDto; onClose: () => void }) {
  const { t } = useTranslation()
  const [key, renew] = useIntentKey()
  const [reason, setReason] = useState('')
  const waive = useWaiveFindingMutation(orgId, { revision: finding.revision, idempotencyKey: key })
  const stale = waive.error instanceof ApiProblem && waive.error.code === 'revision_conflict'
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault()
            waive.mutate(
              { orgId, runId: run.id, fndId: finding.id, data: { reason: reason.trim() } },
              {
                onSuccess: () => {
                  renew()
                  toast.success(t('review.waived'))
                  onClose()
                },
              },
            )
          }}
        >
          <DialogHeader>
            <DialogTitle>{t('review.waiveTitle')}</DialogTitle>
            <DialogDescription>{t('review.waiveHelp')}</DialogDescription>
          </DialogHeader>
          <p className="text-sm" dir="auto">
            {finding.title}
          </p>
          <Field id="waive-reason" label={t('review.waiveReason')}>
            {(f) => <Textarea {...f} required maxLength={2000} value={reason} onChange={(e) => setReason(e.target.value)} />}
          </Field>
          {waive.isError && (
            <Alert variant="destructive" role="alert">
              <AlertDescription>{stale ? t('review.waiveStale') : problemMessage(t, waive.error)}</AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={onClose}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" disabled={waive.isPending || !reason.trim()}>
              {t('review.waive')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
