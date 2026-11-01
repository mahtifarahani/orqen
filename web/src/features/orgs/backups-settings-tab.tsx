import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import type { BackupSettingsDto, RestorePreviewDto } from '@/api/generated/model'
import { newIdempotencyKey } from '@/api/idempotency'
import { useBackups, useBackupSettings, usePrunePreview, usePruneRun, usePutSettings, useRefreshBackups, useStartBackupRun, useVerifyRun } from '@/api/hooks/backups'
import { Field } from '@/components/field'
import { Ltr } from '@/components/ltr'
import { NumberField } from '@/components/number-field'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { useOperationRunner } from '@/components/use-operation-runner'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { formatDateTime, formatNumber } from '@/lib/format'
import { problemMessage } from '@/lib/problem-message'
import { useLocale } from '@/lib/use-locale'
import { RestorePreviewSheet } from './restore-preview-sheet'

type Draft = Pick<BackupSettingsDto, 'enabled' | 'interval_hours' | 'keep_last' | 'keep_days' | 'dir' | 'on_artifact_pattern'>
const toDraft = (s: BackupSettingsDto): Draft => ({ enabled: s.enabled, interval_hours: s.interval_hours, keep_last: s.keep_last, keep_days: s.keep_days, dir: s.dir, on_artifact_pattern: s.on_artifact_pattern })

/** Backup schedule and retention, the list of backups on disk, Back up now, verify (restore preview) and prune. */
export function BackupsSettingsTab({ orgId }: { orgId: string }) {
  const { t } = useTranslation()
  const settings = useBackupSettings(orgId)
  const list = useBackups(orgId)
  return (
    <div className="flex max-w-4xl flex-col gap-8">
      <div>
        <h2 className="text-lg font-semibold">{t('backups.title')}</h2>
        <p className="text-sm text-muted-foreground">{t('backups.subtitle')}</p>
      </div>
      {settings.isPending && <LoadingBlock rows={3} />}
      {settings.isError && <LoadFailed title={t('backups.title')} error={settings.error} onRetry={() => settings.refetch()} />}
      {settings.data && <SettingsForm key={settings.data.revision} orgId={orgId} settings={settings.data} />}
      {list.isPending && <LoadingBlock rows={2} />}
      {list.isError && <LoadFailed title={t('backups.list.title')} error={list.error} onRetry={() => list.refetch()} />}
      {list.data && <BackupList orgId={orgId} data={list.data} />}
    </div>
  )
}

function SettingsForm({ orgId, settings }: { orgId: string; settings: BackupSettingsDto }) {
  const { t } = useTranslation()
  const [draft, setDraft] = useState<Draft>(toDraft(settings))
  const put = usePutSettings(orgId, settings.revision)
  const set = <K extends keyof Draft>(k: K, v: Draft[K]) => setDraft((d) => ({ ...d, [k]: v }))
  const valid = draft.interval_hours >= 1 && draft.interval_hours <= 168 && draft.keep_last >= 1 && draft.keep_last <= 100 && draft.keep_days >= 0 && draft.keep_days <= 3650
  return (
    <form
      className="flex flex-col gap-4"
      onSubmit={(e) => {
        e.preventDefault()
        put.mutate({ orgId, data: draft }, { onSuccess: () => toast.success(t('backups.settings.saved')) })
      }}
    >
      <div className="flex items-center gap-2">
        <Switch id="backup-enabled" checked={draft.enabled} onCheckedChange={(v) => set('enabled', v)} aria-label={t('backups.settings.enabled')} />
        <label htmlFor="backup-enabled" className="text-sm font-medium">{t('backups.settings.enabled')}</label>
      </div>
      <div className="grid gap-4 sm:grid-cols-3">
        <Field id="backup-interval" label={t('backups.settings.interval')} hint={t('backups.settings.intervalHint')}>
          {(f) => <NumberField {...f} value={draft.interval_hours} min={1} max={168} onCommit={(n) => set('interval_hours', n ?? 0)} />}
        </Field>
        <Field id="backup-keep-last" label={t('backups.settings.keepLast')}>
          {(f) => <NumberField {...f} value={draft.keep_last} min={1} max={100} onCommit={(n) => set('keep_last', n ?? 0)} />}
        </Field>
        <Field id="backup-keep-days" label={t('backups.settings.keepDays')} hint={t('backups.settings.keepDaysHint')}>
          {(f) => <NumberField {...f} value={draft.keep_days} min={0} max={3650} onCommit={(n) => set('keep_days', n ?? 0)} />}
        </Field>
      </div>
      <Field id="backup-dir" label={t('backups.settings.dir')} hint={t('backups.settings.dirHint', { dir: `\u2066${settings.default_dir}\u2069` })}>
        {(f) => <Input {...f} dir="ltr" value={draft.dir} onChange={(e) => set('dir', e.target.value)} />}
      </Field>
      <fieldset className="flex flex-col gap-2">
        <legend className="mb-1 text-sm font-medium">{t('backups.settings.pattern')}</legend>
        {(['fail', 'exclude_artifact'] as const).map((v) => (
          <label key={v} className="flex items-start gap-2 text-sm">
            <input type="radio" name="backup-pattern" className="mt-1" checked={draft.on_artifact_pattern === v} onChange={() => set('on_artifact_pattern', v)} />
            <span>{t(`backups.settings.patterns.${v}`)}</span>
          </label>
        ))}
      </fieldset>
      {put.isError && (
        <Alert variant="destructive" role="alert">
          <AlertDescription>{problemMessage(t, put.error)}</AlertDescription>
        </Alert>
      )}
      <div>
        <Button type="submit" disabled={!valid || put.isPending}>{t('common.save')}</Button>
      </div>
    </form>
  )
}

function BackupList({ orgId, data }: { orgId: string; data: NonNullable<ReturnType<typeof useBackups>['data']> }) {
  const { t } = useTranslation()
  const locale = useLocale()
  const runner = useOperationRunner(orgId)
  const start = useStartBackupRun(orgId, runner.key)
  const refresh = useRefreshBackups(orgId)
  const finished = runner.succeeded || runner.failed
  useEffect(() => {
    if (finished) refresh()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [finished])
  const [verifyKey, setVerifyKey] = useState(newIdempotencyKey)
  const verify = useVerifyRun(verifyKey)
  const [preview, setPreview] = useState<RestorePreviewDto | null>(null)
  const [pruning, setPruning] = useState(false)
  return (
    <section className="flex flex-col gap-3" aria-label={t('backups.list.title')}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h3 className="font-medium">{t('backups.list.title')}</h3>
          <p className="text-sm text-muted-foreground">
            <Ltr className="break-all">{data.dir}</Ltr>
            {data.next_due_at && ` · ${t('backups.list.nextDue', { when: formatDateTime(data.next_due_at, locale) })}`}
          </p>
        </div>
        <div className="flex gap-2">
          <Button variant="outline" onClick={() => setPruning(true)}>{t('backups.prune.open')}</Button>
          <Button disabled={start.isPending || runner.running} onClick={() => start.mutate({ orgId }, { onSuccess: runner.started, onError: (e) => toast.error(problemMessage(t, e)) })}>
            {t(runner.running ? 'backups.list.running' : 'backups.list.now')}
          </Button>
        </div>
      </div>
      {runner.failed && <p role="alert" className="text-sm text-destructive">{t('backups.list.failed')}</p>}
      {data.entries.length === 0 && <p className="text-sm text-muted-foreground">{t('backups.list.none')}</p>}
      <ul className="flex flex-col gap-2">
        {data.entries.map((b) => (
          <li key={b.name} className="flex flex-wrap items-center justify-between gap-2 rounded-md border p-3 text-sm">
            <div className="flex flex-wrap items-center gap-2">
              <Ltr className="break-all">{b.name}</Ltr>
              <Badge variant={b.valid ? 'outline' : 'destructive'}>{t(b.valid ? 'backups.list.valid' : 'backups.list.invalid')}</Badge>
              <span className="text-muted-foreground">{b.created_at ? formatDateTime(b.created_at, locale) : ''} · {t('backups.list.bytes', { bytes: formatNumber(b.bytes, locale) })}</span>
            </div>
            <Button size="sm" variant="outline" disabled={verify.isPending} onClick={() => verify.mutate({ orgId, backupName: b.name }, { onSuccess: (res) => { setPreview(res.data as RestorePreviewDto); setVerifyKey(newIdempotencyKey()) }, onError: (e) => toast.error(problemMessage(t, e)) })}>
              {t('backups.list.verify')}
            </Button>
          </li>
        ))}
      </ul>
      {data.runs.length > 0 && (
        <ul className="flex flex-col gap-1 text-sm" aria-label={t('backups.list.runs')}>
          {data.runs.slice(0, 5).map((r) => (
            <li key={r.id} className="flex flex-wrap items-center gap-2 text-muted-foreground">
              <Badge variant={r.state === 'failed' ? 'destructive' : 'outline'}>{t(`backups.runState.${r.state}`)}</Badge>
              <span>{t(`backups.trigger.${r.trigger}`)}</span>
              <span>{formatDateTime(r.started_at, locale)}</span>
              {r.failure && <span className="text-destructive">{t(`problems.${r.failure.code}`, { defaultValue: r.failure.code })}</span>}
            </li>
          ))}
        </ul>
      )}
      <RestorePreviewSheet preview={preview} dir={data.dir} onClose={() => setPreview(null)} />
      {pruning && <PruneDialog orgId={orgId} onClose={() => setPruning(false)} />}
    </section>
  )
}

function PruneDialog({ orgId, onClose }: { orgId: string; onClose: () => void }) {
  const { t } = useTranslation()
  const preview = usePrunePreview(orgId, true)
  const [key, setKey] = useState(newIdempotencyKey)
  const prune = usePruneRun(orgId, key)
  const doomed = preview.data?.delete ?? []
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('backups.prune.title')}</DialogTitle>
          <DialogDescription>{t('backups.prune.help')}</DialogDescription>
        </DialogHeader>
        {preview.isPending && <LoadingBlock rows={2} />}
        {preview.isError && <p role="alert" className="text-sm text-destructive">{problemMessage(t, preview.error)}</p>}
        {preview.data && (
          <div className="flex flex-col gap-2 text-sm">
            <p>{t('backups.prune.summary', { remove: doomed.length, keep: preview.data.keep.length })}</p>
            <ul className="max-h-48 overflow-auto">
              {doomed.map((n) => (
                <li key={n}><Ltr className="break-all">{n}</Ltr></li>
              ))}
            </ul>
          </div>
        )}
        {prune.isError && <p role="alert" className="text-sm text-destructive">{problemMessage(t, prune.error)}</p>}
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>{t('common.cancel')}</Button>
          <Button
            variant="destructive"
            disabled={doomed.length === 0 || prune.isPending}
            onClick={() => prune.mutate({ orgId, data: { confirm: true } }, { onSuccess: () => { setKey(newIdempotencyKey()); toast.success(t('backups.prune.done')); onClose() } })}
          >
            {t('backups.prune.confirm', { count: doomed.length })}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
