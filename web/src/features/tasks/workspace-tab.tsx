import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Trash2Icon } from 'lucide-react'
import { toast } from 'sonner'
import type { RunDto } from '@/api/generated/model'
import { useDiscardWorkspace } from '@/api/hooks/tasks'
import { Ltr } from '@/components/ltr'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { formatDateTime } from '@/lib/format'
import { problemMessage } from '@/lib/problem-message'
import { useIntentKey } from '@/lib/use-intent-key'
import { useLocale } from '@/lib/use-locale'

export function WorkspaceTab({ orgId, run }: { orgId: string; run: RunDto | undefined }) {
  const { t } = useTranslation()
  const locale = useLocale()
  const ws = run?.workspace
  const [key, renew] = useIntentKey()
  const discard = useDiscardWorkspace(orgId, { revision: ws?.revision ?? 0, idempotencyKey: key })
  const [step, setStep] = useState<0 | 1 | 2>(0)
  if (!run) return <p className="text-sm text-muted-foreground">{t('taskDetail.noRun')}</p>
  if (!ws) return <p className="text-sm text-muted-foreground">{t('workspace.none')}</p>
  const canDiscard = !run.active && ws.state !== 'removed'
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex flex-wrap items-center gap-2">
          {t('workspace.title')}
          <Badge variant="outline" data-workspace-state={ws.state}>
            {t(`workspace.state.${ws.state}`)}
          </Badge>
          {ws.dirty && <Badge variant="destructive">{t('workspace.dirty')}</Badge>}
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-3 text-sm">
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1">
          <dt className="text-muted-foreground">{t('workspace.branch')}</dt>
          <dd>
            <Ltr>{ws.branch}</Ltr>
          </dd>
          <dt className="text-muted-foreground">{t('workspace.base')}</dt>
          <dd>
            <Ltr>{ws.base_ref}</Ltr> <Ltr>{ws.base_sha.slice(0, 12)}</Ltr>
          </dd>
          <dt className="text-muted-foreground">{t('workspace.head')}</dt>
          <dd>
            <Ltr>{ws.head_sha.slice(0, 12)}</Ltr>
          </dd>
          <dt className="text-muted-foreground">{t('workspace.path')}</dt>
          <dd className="break-all">
            <Ltr>{ws.path || '—'}</Ltr>
          </dd>
          <dt className="text-muted-foreground">{t('workspace.created')}</dt>
          <dd>{formatDateTime(ws.created_at, locale)}</dd>
        </dl>
        {ws.snapshot_artifact_id && <p className="text-muted-foreground">{t('workspace.snapshotKept')}</p>}
        {ws.state === 'preserved' && (
          <Alert>
            <AlertDescription>{t('workspace.preservedBody')}</AlertDescription>
          </Alert>
        )}
        {canDiscard && (
          <div>
            <Button variant="outline" onClick={() => setStep(1)}>
              <Trash2Icon />
              {t('workspace.discard')}
            </Button>
          </div>
        )}
        <Dialog open={step > 0} onOpenChange={(open) => !open && setStep(0)}>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>{t('workspace.discardDialog.title')}</DialogTitle>
              <DialogDescription>{step === 1 ? t('workspace.discardDialog.first') : t('workspace.discardDialog.second')}</DialogDescription>
            </DialogHeader>
            <DialogFooter>
              <Button variant="outline" onClick={() => setStep(0)}>
                {t('common.cancel')}
              </Button>
              {step === 1 ? (
                <Button variant="destructive" onClick={() => setStep(2)}>
                  {t('workspace.discardDialog.continue')}
                </Button>
              ) : (
                <Button
                  variant="destructive"
                  disabled={discard.isPending}
                  onClick={() =>
                    discard.mutate(
                      { orgId, wspId: ws.id },
                      {
                        onSuccess: () => {
                          renew()
                          setStep(0)
                          toast.success(t('workspace.discardStarted'))
                        },
                        onError: (e) => toast.error(problemMessage(t, e)),
                      },
                    )
                  }
                >
                  {t('workspace.discardDialog.confirm')}
                </Button>
              )}
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </CardContent>
    </Card>
  )
}
