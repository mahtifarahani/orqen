import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import type { ExternalOperationDto, RunDto } from '@/api/generated/model'
import { useExternalWrites } from '@/api/hooks/externalWrites'
import { useApproveOperations, useOperationDetail, useRunOperations } from '@/api/hooks/runDetail'
import { ApiProblem } from '@/api/problem'
import { Ltr } from '@/components/ltr'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { problemMessage } from '@/lib/problem-message'
import { useIntentKey } from '@/lib/use-intent-key'

/** Operations the user can approve: waiting ones, and simulated ones once live writes are on. */
const approvable = (state: string, live: boolean) => state === 'awaiting_approval' || (live && state === 'dry_run')

/** Every external operation of the run, the exact payload of each, and the approval dialog (live mode only). */
export function PublishingTab({ orgId, run }: { orgId: string; run: RunDto | undefined }) {
  const { t } = useTranslation()
  const ops = useRunOperations(orgId, run?.id)
  const settings = useExternalWrites(orgId)
  const live = settings.data?.mode === 'live' && !settings.data.paused
  const [open, setOpen] = useState<string | null>(null)
  if (!run) return <p className="text-sm text-muted-foreground">{t('taskDetail.noRun')}</p>
  if (ops.isPending) return <LoadingBlock rows={2} />
  if (ops.isError) return <LoadFailed title={t('xop.loadFailed')} error={ops.error} onRetry={() => void ops.refetch()} />
  if (ops.data.length === 0) return <p className="text-sm text-muted-foreground">{t('xop.none')}</p>
  const waiting = ops.data.filter((o) => approvable(o.state, live))
  return (
    <div className="flex flex-col gap-4">
      {run.publish_mode === 'dry_run' && !live && (
        <Alert>
          <AlertTitle>{t('xop.dryRunTitle')}</AlertTitle>
          <AlertDescription>{t('xop.dryRunBody')}</AlertDescription>
        </Alert>
      )}
      {waiting.length > 0 && (
        <Button className="self-start" onClick={() => setOpen(waiting[0]!.id)}>
          {t('xop.review', { n: waiting.length })}
        </Button>
      )}
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{t('xop.action')}</TableHead>
            <TableHead>{t('xop.target')}</TableHead>
            <TableHead>{t('xop.state')}</TableHead>
            <TableHead>{t('xop.round')}</TableHead>
            <TableHead>
              <span className="sr-only">{t('xop.details')}</span>
            </TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {ops.data.map((o) => (
            <TableRow key={o.id} data-xop-state={o.state}>
              <TableCell>{t(`xop.actions.${o.action}`, { defaultValue: o.action })}</TableCell>
              <TableCell>
                <Ltr>{o.target.project_path}</Ltr> <Ltr className="text-muted-foreground">{o.target.branch}</Ltr>
              </TableCell>
              <TableCell>
                <Badge variant="outline">{t(`xop.states.${o.state}`, { defaultValue: o.state })}</Badge>
                {o.error && <span className="ms-2 text-xs text-destructive">{o.error.code}</span>}
              </TableCell>
              <TableCell>{o.round}</TableCell>
              <TableCell>
                <Button size="sm" variant="ghost" onClick={() => setOpen(o.id)}>
                  {t('xop.details')}
                </Button>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
      {open && <OperationDialog orgId={orgId} run={run} ops={ops.data} live={live} id={open} onClose={() => setOpen(null)} onSelect={setOpen} />}
    </div>
  )
}

function OperationDialog({
  orgId,
  run,
  ops,
  live,
  id,
  onClose,
  onSelect,
}: {
  orgId: string
  run: RunDto
  ops: ExternalOperationDto[]
  live: boolean
  id: string
  onClose: () => void
  onSelect: (id: string) => void
}) {
  const { t } = useTranslation()
  const detail = useOperationDetail(orgId, id)
  const [key, renew] = useIntentKey()
  const approve = useApproveOperations(orgId, { revision: run.revision, idempotencyKey: key })
  const stale = approve.error instanceof ApiProblem && (approve.error.code === 'intent_changed' || approve.error.code === 'revision_conflict')
  const op = detail.data
  const waiting = ops.filter((o) => approvable(o.state, live))
  const canApprove = !!op && approvable(op.state, live)
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90vh] overflow-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t('xop.dialogTitle')}</DialogTitle>
          <DialogDescription>{t('xop.dialogHelp')}</DialogDescription>
        </DialogHeader>
        {detail.isPending && <LoadingBlock rows={2} />}
        {detail.isError && <LoadFailed title={t('xop.loadFailed')} error={detail.error} onRetry={() => void detail.refetch()} />}
        {op && (
          <div className="flex flex-col gap-3 text-sm">
            <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1">
              <dt className="text-muted-foreground">{t('xop.action')}</dt>
              <dd>{t(`xop.actions.${op.action}`, { defaultValue: op.action })}</dd>
              <dt className="text-muted-foreground">{t('xop.host')}</dt>
              <dd>
                <Ltr>{op.target.host}</Ltr>
              </dd>
              <dt className="text-muted-foreground">{t('xop.project')}</dt>
              <dd>
                <Ltr>{op.target.project_path}</Ltr>
              </dd>
              <dt className="text-muted-foreground">{t('xop.branch')}</dt>
              <dd>
                <Ltr>{op.target.branch}</Ltr>
              </dd>
              {op.preview.refspec && (
                <>
                  <dt className="text-muted-foreground">{t('xop.refspec')}</dt>
                  <dd>
                    <Ltr>{op.preview.refspec}</Ltr>
                  </dd>
                </>
              )}
              <dt className="text-muted-foreground">{t('xop.intent')}</dt>
              <dd>
                <Ltr className="font-mono text-xs">{op.intent_hash.slice(0, 16)}</Ltr>
              </dd>
            </dl>
            {op.preview.title && <p className="font-medium" dir="auto">{op.preview.title}</p>}
            {op.preview.body && (
              <pre className="max-h-60 overflow-auto whitespace-pre-wrap rounded-md border bg-muted p-3 text-xs" dir="auto">
                {op.preview.body}
              </pre>
            )}
            {op.preview.diffstat && (
              <pre className="overflow-auto rounded-md border bg-muted p-3 text-xs" dir="ltr">
                {op.preview.diffstat}
              </pre>
            )}
            {op.preview.commits.length > 0 && (
              <ul className="list-disc ps-5" aria-label={t('xop.commits')}>
                {op.preview.commits.map((c, i) => (
                  <li key={i}>
                    <Ltr className="font-mono text-xs">{String((c as { sha?: string }).sha ?? '').slice(0, 12)}</Ltr> <span dir="auto">{String((c as { subject?: string }).subject ?? '')}</span>
                  </li>
                ))}
              </ul>
            )}
            {op.decision && op.decision.reasons.length > 0 && (
              <p className="text-muted-foreground">
                {t('xop.decision')}: {op.decision.reasons.map((r) => t(`xop.reasons.${r}`, { defaultValue: r })).join(', ')}
              </p>
            )}
          </div>
        )}
        {approve.isError && (
          <Alert variant="destructive" role="alert">
            <AlertDescription>{stale ? t('xop.stale') : problemMessage(t, approve.error)}</AlertDescription>
          </Alert>
        )}
        <DialogFooter className="flex-wrap">
          {waiting.length > 1 && canApprove && (
            <Button type="button" variant="outline" onClick={() => onSelect(waiting.find((o) => o.id !== id)!.id)}>
              {t('xop.next')}
            </Button>
          )}
          <Button type="button" variant="ghost" onClick={onClose}>
            {t('common.close')}
          </Button>
          {canApprove && op && (
            <Button
              disabled={approve.isPending}
              onClick={() =>
                approve.mutate(
                  { orgId, runId: run.id, data: { operations: [{ id: op.id, intent_hash: op.intent_hash }] } },
                  {
                    onSuccess: () => {
                      renew()
                      toast.success(t('xop.approved'))
                      onClose()
                    },
                  },
                )
              }
            >
              {t('xop.approve')}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
