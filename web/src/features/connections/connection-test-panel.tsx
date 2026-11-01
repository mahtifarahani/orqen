import { useState, type RefObject } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { CheckIcon, DownloadIcon, Loader2Icon, MinusIcon, PlayIcon, XIcon, CircleDashedIcon } from 'lucide-react'
import { useOperation, useTestConnection, type Connection } from '@/api/hooks/connections'
import { getGetArtifactContentUrl } from '@/api/generated/artifacts/artifacts'
import type { StageResult } from '@/api/generated/model'
import { newIdempotencyKey } from '@/api/idempotency'
import { ok } from '@/api/problem'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { StatusBadge } from '@/components/status-badge'
import { useEventFeed } from '@/components/events-context'
import { Ltr } from '@/components/ltr'
import { problemMessage } from '@/lib/problem-message'

const STAGES = ['network', 'tls', 'http', 'credential', 'auth', 'identity', 'capabilities', 'sample_access'] as const

type Shown = StageResult['status'] | 'pending' | 'running'

function StageIcon({ status }: { status: Shown }) {
  const cls = 'size-4 shrink-0'
  switch (status) {
    case 'passed':
      return <CheckIcon className={`${cls} text-emerald-600`} aria-hidden />
    case 'failed':
      return <XIcon className={`${cls} text-destructive`} aria-hidden />
    case 'skipped':
      return <MinusIcon className={`${cls} text-muted-foreground`} aria-hidden />
    case 'running':
      return <Loader2Icon className={`${cls} animate-spin`} aria-hidden />
    default:
      return <CircleDashedIcon className={`${cls} text-muted-foreground`} aria-hidden />
  }
}

export function ConnectionTestPanel({
  orgId,
  connection,
  open,
  onOpenChange,
  returnFocusRef,
}: {
  orgId: string
  connection: Connection
  open: boolean
  onOpenChange: (open: boolean) => void
  returnFocusRef?: RefObject<HTMLElement | null>
}) {
  const { t } = useTranslation()
  // One key per click intent: kept on failure (a retry replays), renewed once the call succeeded.
  const [key, setKey] = useState(newIdempotencyKey)
  const [opId, setOpId] = useState<string | null>(null)
  const start = useTestConnection(key)
  const lastTest = connection.last_test
  const activeOp = opId ?? lastTest?.operation_id ?? null
  const operation = useOperation(orgId, activeOp)
  const feed = useEventFeed()

  const live = new Map<string, StageResult>()
  for (const ev of feed.events) {
    if (ev.type === 'connection_test.stage' && ev.operation_id === activeOp) {
      const s = ev.payload as unknown as StageResult
      live.set(s.stage, s)
    }
  }
  if (lastTest && lastTest.operation_id === activeOp) {
    for (const s of lastTest.stages) if (!live.has(s.stage)) live.set(s.stage, s)
  }

  const op = operation.data
  const running = start.isPending || op?.status === 'pending' || op?.status === 'running'
  const firstMissing = STAGES.find((s) => !live.has(s))
  const result = (op?.result ?? null) as { overall?: string; artifact_id?: string } | null
  const artifactId = result?.artifact_id ?? (lastTest?.operation_id === activeOp ? lastTest?.artifact_id : null)
  const overall = result?.overall ?? (lastTest?.operation_id === activeOp ? lastTest?.overall : undefined)

  const run = () => {
    start.mutate(
      { orgId, conId: connection.id },
      {
        onSuccess: (res) => {
          const o = ok(res)
          setOpId(o.id)
          setKey(newIdempotencyKey())
          if (o.deduplicated) toast.info(t('test.deduplicated'))
          else if ('headers' in res && res.headers.get('idempotent-replayed') === 'true') toast.info(t('test.replayed'))
          else toast.success(t('test.started'))
        },
      },
    )
  }

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        side="end"
        onCloseAutoFocus={(e) => {
          if (returnFocusRef?.current) {
            e.preventDefault()
            returnFocusRef.current.focus()
          }
        }}
 className="w-full overflow-y-auto data-[side=end]:sm:max-w-md">
        <SheetHeader>
          <SheetTitle>{t('test.title')}</SheetTitle>
          <SheetDescription>{t('test.description')}</SheetDescription>
        </SheetHeader>
        <div className="flex flex-col gap-4 px-4 pb-4">
          <p className="text-sm">
            <span className="font-medium">{connection.name}</span> <Ltr className="text-xs">{connection.base_url}</Ltr>
          </p>
          <div>
            <Button onClick={run} disabled={running}>
              {running ? <Loader2Icon className="animate-spin" /> : <PlayIcon className="rtl:-scale-x-100" />}
              {running ? t('test.running') : activeOp ? t('test.rerun') : t('test.run')}
            </Button>
          </div>
          {start.isError && (
            <Alert variant="destructive" role="alert">
              <AlertDescription>{problemMessage(t, start.error)}</AlertDescription>
            </Alert>
          )}
          {op?.status === 'failed' && (
            <Alert variant="destructive" role="alert">
              <AlertDescription>{t('test.failedOperation')}</AlertDescription>
            </Alert>
          )}
          {!activeOp && <p className="text-sm text-muted-foreground">{t('test.noResult')}</p>}
          {activeOp && (
            <ol className="flex flex-col divide-y rounded-lg border" aria-label={t('test.title')} aria-live="polite">
              {STAGES.map((name) => {
                const r = live.get(name)
                const shown: Shown = r ? r.status : running && name === firstMissing ? 'running' : 'pending'
                return (
                  <li key={name} className="flex items-start gap-3 p-3" data-stage={name} data-status={shown}>
                    <StageIcon status={shown} />
                    <div className="flex min-w-0 flex-1 flex-col gap-0.5">
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="text-sm font-medium">{t(`test.stages.${name}`)}</span>
                        {shown === 'not_implemented' ? (
                          <Badge variant="secondary">{t('test.stageStatus.not_implemented')}</Badge>
                        ) : (
                          <span className="text-xs text-muted-foreground">{t(`test.stageStatus.${shown}`)}</span>
                        )}
                      </div>
                      {r && shown !== 'not_implemented' && r.detail && (
                        <p className="flex flex-wrap gap-x-2 break-words text-xs text-muted-foreground">
                          {r.code && <Ltr>{r.code}</Ltr>}
                          <Ltr className="font-sans">{r.detail}</Ltr>
                        </p>
                      )}
                    </div>
                    {r && <span className="text-xs text-muted-foreground tabular-nums">{t('test.duration', { ms: r.duration_ms })}</span>}
                  </li>
                )
              })}
            </ol>
          )}
          {overall && (
            <div className="flex items-center gap-2 text-sm">
              <span className="font-medium">{t('test.overall')}</span>
              <StatusBadge status={overall} />
            </div>
          )}
          {(connection.identity || connection.capabilities) && (
            <section aria-label={t('test.verified')} className="flex flex-col gap-2 rounded-lg border p-3 text-sm">
              {connection.identity && (
                <p>
                  <span className="font-medium">{t('test.identity')}: </span>
                  <Ltr className="font-sans">{connection.identity.display_name ? `${connection.identity.display_name} (${connection.identity.username})` : connection.identity.username}</Ltr>
                </p>
              )}
              {connection.capabilities && (
                <>
                  {(connection.capabilities.server_version || connection.capabilities.deployment_detected) && (
                    <p>
                      <span className="font-medium">{t('test.server')}: </span>
                      <Ltr className="font-sans">{[connection.capabilities.deployment_detected, connection.capabilities.server_version].filter(Boolean).join(' ')}</Ltr>
                    </p>
                  )}
                  {connection.capabilities.token_scopes && connection.capabilities.token_scopes.length > 0 && (
                    <p>
                      <span className="font-medium">{t('test.scopes')}: </span>
                      <Ltr className="font-sans">{connection.capabilities.token_scopes.join(', ')}</Ltr>
                    </p>
                  )}
                  <ul className="flex flex-wrap gap-1.5" aria-label={t('test.features')}>
                    {Object.entries(connection.capabilities.features).map(([k, v]) => (
                      <li key={k}>
                        <Badge variant={v === 'available' ? 'secondary' : 'outline'}>
                          <Ltr className="font-sans">{k}</Ltr>: {t(`test.featureStates.${v}`, { defaultValue: v })}
                        </Badge>
                      </li>
                    ))}
                  </ul>
                </>
              )}
            </section>
          )}
          {artifactId && (
            <Button asChild variant="outline" className="self-start">
              <a href={getGetArtifactContentUrl(orgId, artifactId)} download>
                <DownloadIcon />
                {t('test.downloadReport')}
              </a>
            </Button>
          )}
        </div>
      </SheetContent>
    </Sheet>
  )
}
