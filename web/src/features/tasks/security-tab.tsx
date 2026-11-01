import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import type { RunDto } from '@/api/generated/model'
import { useRunSecurityFindings, useSecurityScans, useSecurityStatus, useStartScan } from '@/api/hooks/security'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { useOperationRunner } from '@/components/use-operation-runner'
import { Button } from '@/components/ui/button'
import { problemMessage } from '@/lib/problem-message'
import { ScanList } from '@/features/security/scan-list'
import { scanActive } from '@/features/security/scan-active'
import { SecurityFindings } from '@/features/security/findings-list'

/** The security scans and findings of this run, and the button that scans its reviewed head. */
export function SecurityTab({ orgId, run }: { orgId: string; run: RunDto | undefined }) {
  const { t } = useTranslation()
  const runId = run?.id
  const scans = useSecurityScans(orgId, { run_id: runId }, !!runId)
  const findings = useRunSecurityFindings(orgId, runId)
  const status = useSecurityStatus(orgId)
  const runner = useOperationRunner(orgId)
  const start = useStartScan(orgId, runner.key)
  if (!run) return <p className="text-sm text-muted-foreground">{t('taskDetail.noRun')}</p>
  const running = (scans.data ?? []).some(scanActive) || runner.running
  const unusable = status.data && !status.data.usable.ok ? status.data.usable.reason : null
  const reviewed = !!run.heads?.reviewed
  const blocked = unusable ? t(`extensions.unusable.${unusable}`, { defaultValue: t(`problems.${unusable}`, { defaultValue: unusable }) }) : !reviewed ? t('security.run.needsReview') : null
  return (
    <div className="flex flex-col gap-6">
      <section className="flex flex-col gap-2">
        <div className="flex flex-wrap items-center gap-3">
          <Button
            disabled={!!blocked || running || start.isPending}
            onClick={() => start.mutate({ orgId, runId: run.id, data: { target_kind: 'local_clone' } }, { onSuccess: runner.started, onError: (e) => toast.error(problemMessage(t, e)) })}
          >
            {t(running ? 'security.run.scanning' : 'security.run.scan')}
          </Button>
          {blocked && <span className="text-sm text-muted-foreground" data-testid="scan-blocked">{blocked}</span>}
        </div>
        <p className="text-sm text-muted-foreground">{t('security.run.help')}</p>
      </section>
      <section className="flex flex-col gap-2" aria-label={t('security.scans.title')}>
        <h3 className="font-medium">{t('security.scans.title')}</h3>
        {scans.isPending && <LoadingBlock rows={2} />}
        {scans.isError && <LoadFailed title={t('security.scans.title')} error={scans.error} onRetry={() => scans.refetch()} />}
        {scans.data && <ScanList orgId={orgId} scans={scans.data} />}
      </section>
      <section className="flex flex-col gap-2" aria-label={t('security.findings.title')}>
        <h3 className="font-medium">{t('security.findings.title')}</h3>
        {findings.isPending && <LoadingBlock rows={2} />}
        {findings.data && <SecurityFindings orgId={orgId} findings={findings.data} />}
      </section>
    </div>
  )
}
