import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router'
import { CheckIcon, CircleAlertIcon, CopyIcon, MinusIcon, RefreshCwIcon, TriangleAlertIcon } from 'lucide-react'
import { toast } from 'sonner'
import type { PreflightCheck } from '@/api/generated/model'
import { useKeychainProbe, useLaunchdOptIn, useLaunchdPlan, usePreflight, useStartPreflightRun } from '@/api/hooks/system'
import { newIdempotencyKey } from '@/api/idempotency'
import { ok } from '@/api/problem'
import { Ltr } from '@/components/ltr'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { useOperationRunner } from '@/components/use-operation-runner'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { conflictCurrent } from '@/lib/conflict'
import { formatDateTime } from '@/lib/format'
import { problemMessage } from '@/lib/problem-message'
import { useLocale } from '@/lib/use-locale'

const DAY = 24 * 3600 * 1000
// Fix codes that are solved in the Orchestrator & keys tab.
const KEY_FIXES = ['jev_key_missing', 'jev_key_refused', 'backend_key_missing']
const ICON = { pass: CheckIcon, warn: TriangleAlertIcon, fail: CircleAlertIcon, skipped: MinusIcon } as const

/** Text for a fix code; binary checks share generic wording with the tool's name filled in. */
function useFixText() {
  const { t } = useTranslation()
  return (code: string): string => {
    if (!code) return ''
    const exact = t(`wizard.fix.${code}`, { defaultValue: '' })
    if (exact) return exact
    const m = /^(.+)_(missing|unreadable|too_old)$/.exec(code)
    if (m) return t(`wizard.fix.binary_${m[2]}`, { name: m[1] })
    return code
  }
}

function CopyCommand({ command }: { command: string }) {
  const { t } = useTranslation()
  return (
    <span className="flex flex-wrap items-center gap-2">
      <Ltr className="rounded bg-muted px-2 py-1 text-xs">{command}</Ltr>
      <Button
        type="button"
        size="sm"
        variant="outline"
        onClick={() => {
          // Copying only puts the text on the clipboard; Orqen never runs it.
          void navigator.clipboard?.writeText(command).then(() => toast.success(t('system.copied')))
        }}
      >
        <CopyIcon />
        {t('system.copy')}
        <span className="sr-only">: {command}</span>
      </Button>
    </span>
  )
}

/**
 * Detect-only system check. It reads the stored report, starts a new preflight only when asked (or once on entry
 * to the wizard step when there is no report from the last 24 hours), and shows fixes as commands to copy.
 */
export function SystemCheckPanel({ orgId, embedded = false }: { orgId: string; embedded?: boolean }) {
  const { t } = useTranslation()
  const locale = useLocale()
  const fix = useFixText()
  const report = usePreflight(orgId)
  const runner = useOperationRunner(orgId)
  const start = useStartPreflightRun(runner.key)
  const entered = useRef(false)
  const run = () => start.mutate({ orgId }, { onSuccess: runner.started, onError: (e) => toast.error(problemMessage(t, e)) })

  // Entering the wizard step with no recent report starts one check (once per entry).
  const [now] = useState(() => Date.now())
  useEffect(() => {
    if (!embedded || entered.current || !report.isSuccess) return
    entered.current = true
    const at = report.data.checked_at ? new Date(report.data.checked_at).getTime() : 0
    if (now - at > DAY) run()
  })

  const checks = report.data?.checks ?? []
  const groups = [...new Set(checks.map((c) => c.group))]
  const running = start.isPending || runner.running
  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-3">
        <Button variant="outline" onClick={run} disabled={running}>
          <RefreshCwIcon className={running ? 'animate-spin' : undefined} />
          {running ? t('system.checking') : t('system.recheck')}
        </Button>
        {report.data?.checked_at && <span className="text-xs text-muted-foreground">{t('system.checkedAt', { time: formatDateTime(report.data.checked_at, locale) })}</span>}
      </div>
      <p className="max-w-prose text-xs text-muted-foreground">{t('system.detectOnly')}</p>
      {runner.failed && (
        <Alert variant="destructive" role="alert">
          <AlertDescription>{t('system.failedRun')}</AlertDescription>
        </Alert>
      )}
      {report.isPending && <LoadingBlock />}
      {report.isError && <LoadFailed title={t('system.loadFailed')} error={report.error} onRetry={() => void report.refetch()} />}
      {report.isSuccess && checks.length === 0 && !running && <p className="text-sm text-muted-foreground">{t('system.empty')}</p>}
      {groups.map((g) => (
        <section key={g} aria-labelledby={`sys-${g}`} className="flex flex-col gap-2">
          <h3 id={`sys-${g}`} className="text-sm font-medium">
            {t(`system.groups.${g}`, { defaultValue: g })}
          </h3>
          <ul className="flex flex-col divide-y rounded-lg border">
            {checks.filter((c) => c.group === g).map((c) => (
              <CheckRow key={c.id} orgId={orgId} check={c} fixText={fix(c.fix_code)} />
            ))}
          </ul>
        </section>
      ))}
      <LaunchdSection orgId={orgId} />
    </div>
  )
}

function CheckRow({ orgId, check: c, fixText }: { orgId: string; check: PreflightCheck; fixText: string }) {
  const { t } = useTranslation()
  const Icon = ICON[c.status]
  const label = t(`preflight.check.${c.id}`, { defaultValue: '' })
  return (
    <li className="flex flex-col gap-1.5 p-3 text-sm" data-check={c.id} data-status={c.status}>
      <span className="flex flex-wrap items-center gap-2">
        <Icon className="size-4 shrink-0" aria-hidden />
        <Badge variant={c.status === 'fail' ? 'destructive' : 'outline'}>{t(`system.status.${c.status}`)}</Badge>
        {label && <span className="font-medium">{label}</span>}
        <Ltr className={label ? 'text-xs text-muted-foreground' : 'font-medium'}>{c.id}</Ltr>
        {c.required && <Badge variant="secondary">{t('system.required')}</Badge>}
      </span>
      {c.detail && <span className="text-muted-foreground">{c.detail}</span>}
      {c.status !== 'pass' && c.blocks.length > 0 && (
        <span className="text-xs text-muted-foreground">
          {t('system.blocks')}: {c.blocks.map((b) => (b.startsWith('connection:') ? t('system.features.connection') : t(`system.features.${b}`, { defaultValue: b }))).join(', ')}
        </span>
      )}
      {fixText && (c.status === 'fail' || c.status === 'warn') && <span>{fixText}</span>}
      {KEY_FIXES.includes(c.fix_code) && (
        <Link to={`/orgs/${orgId}/models?tab=orchestrator`} className="w-fit text-xs underline underline-offset-4">
          {t('system.openKeys')}
        </Link>
      )}
      {c.fix_command.length > 0 && (c.status === 'fail' || c.status === 'warn') && <CopyCommand command={c.fix_command.join(' ')} />}
      {c.id === 'keychain.access' && <KeychainTest orgId={orgId} />}
    </li>
  )
}

function KeychainTest({ orgId }: { orgId: string }) {
  const { t } = useTranslation()
  const [key, setKey] = useState(newIdempotencyKey)
  const probe = useKeychainProbe(key)
  const result = probe.data ? ok(probe.data) : null
  return (
    <div className="flex flex-col items-start gap-1">
      <p className="text-xs text-muted-foreground">{t('system.keychainWarn')}</p>
      <Button
        type="button"
        size="sm"
        variant="outline"
        disabled={probe.isPending}
        onClick={() => probe.mutate({ orgId }, { onSuccess: () => setKey(newIdempotencyKey()), onError: (e) => toast.error(problemMessage(t, e)) })}
      >
        {t('system.keychainTest')}
      </Button>
      {result && (
        <span role="status" className="text-xs">
          {t(`system.status.${result.status}`)}: {result.detail}
        </span>
      )}
    </div>
  )
}

function LaunchdSection({ orgId }: { orgId: string }) {
  const { t } = useTranslation()
  const plan = useLaunchdPlan(orgId)
  const optIn = useLaunchdOptIn(orgId, plan.data?.revision ?? 0)
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('system.launchd.title')}</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-3 text-sm">
        <p className="max-w-prose text-muted-foreground">{t('system.launchd.help')}</p>
        {plan.isPending && <LoadingBlock rows={1} />}
        {plan.isError && <LoadFailed title={t('system.launchd.loadFailed')} error={plan.error} onRetry={() => void plan.refetch()} />}
        {plan.data && (
          <>
            <div className="flex items-center gap-2">
              <Switch
                id="launchd-opt"
                checked={plan.data.opted_in}
                disabled={optIn.isPending}
                onCheckedChange={(v) =>
                  optIn.mutate(
                    { orgId, data: { opted_in: v } },
                    { onError: (e) => toast.error(conflictCurrent(e) ? t('problems.revision_conflict') : problemMessage(t, e)) },
                  )
                }
              />
              <Label htmlFor="launchd-opt">{t('system.launchd.optIn')}</Label>
            </div>
            {plan.data.opted_in && (
              <div className="flex flex-col gap-3">
                <p>
                  {t('system.launchd.path')}: <Ltr>{plan.data.plist_path}</Ltr>
                </p>
                <pre dir="ltr" className="max-h-64 overflow-auto rounded-lg border bg-muted/40 p-3 text-start font-mono text-xs whitespace-pre-wrap">
                  {plan.data.plist_xml}
                </pre>
                <h4 className="font-medium">{t('system.launchd.install')}</h4>
                <ul className="flex flex-col gap-2">
                  {plan.data.install_commands.map((c) => (
                    <li key={c}>
                      <CopyCommand command={c} />
                    </li>
                  ))}
                </ul>
                <h4 className="font-medium">{t('system.launchd.uninstall')}</h4>
                <ul className="flex flex-col gap-2">
                  {plan.data.uninstall_commands.map((c) => (
                    <li key={c}>
                      <CopyCommand command={c} />
                    </li>
                  ))}
                </ul>
                <p className="text-xs text-muted-foreground">{t('system.launchd.never')}</p>
              </div>
            )}
          </>
        )}
      </CardContent>
    </Card>
  )
}
