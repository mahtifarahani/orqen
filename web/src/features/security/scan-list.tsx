import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import type { SecurityScanDto } from '@/api/generated/model'
import { useCancelScan, useScanFindings } from '@/api/hooks/security'
import { Ltr } from '@/components/ltr'
import { LoadingBlock } from '@/components/query-state'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { formatDateTime } from '@/lib/format'
import { problemMessage } from '@/lib/problem-message'
import { useLocale } from '@/lib/use-locale'
import { SecurityFindings } from './findings-list'
import { scanActive } from './scan-active'

/** Scans, newest first. Opening one shows its findings in a sheet. */
export function ScanList({ orgId, scans }: { orgId: string; scans: SecurityScanDto[] }) {
  const { t } = useTranslation()
  const locale = useLocale()
  const [open, setOpen] = useState<SecurityScanDto | null>(null)
  if (scans.length === 0) return <p className="text-sm text-muted-foreground">{t('security.scans.none')}</p>
  return (
    <>
      <ul className="flex flex-col gap-2" aria-label={t('security.scans.title')}>
        {scans.map((s) => (
          <li key={s.id} className="flex flex-wrap items-center justify-between gap-2 rounded-md border p-3 text-sm">
            <div className="flex flex-wrap items-center gap-2">
              <Badge variant={s.state === 'finished' && s.completion === 'complete' ? 'default' : 'secondary'} data-scan-state={s.state}>
                {t(`security.scans.state.${s.state}`)}
              </Badge>
              {s.trigger === 'auto' && <Badge variant="outline">{t('security.scans.trigger.auto')}</Badge>}
              {s.completion && <Badge variant="outline">{t(`security.scans.completion.${s.completion}`)}</Badge>}
              <Ltr>{s.head_sha.slice(0, 12)}</Ltr>
              <span className="text-muted-foreground">{formatDateTime(s.finished_at ?? s.created_at, locale)}</span>
              {s.counts.blocking > 0 && <Badge variant="destructive">{t('security.scans.blocking', { count: s.counts.blocking })}</Badge>}
              {s.tree_modified && <Badge variant="outline">{t('security.scans.treeModified')}</Badge>}
            </div>
            <Button size="sm" variant="outline" onClick={() => setOpen(s)}>
              {t('security.scans.open')}
            </Button>
          </li>
        ))}
      </ul>
      <ScanSheet orgId={orgId} scan={open ? (scans.find((s) => s.id === open.id) ?? open) : null} onClose={() => setOpen(null)} />
    </>
  )
}

function ScanSheet({ orgId, scan, onClose }: { orgId: string; scan: SecurityScanDto | null; onClose: () => void }) {
  const { t } = useTranslation()
  const findings = useScanFindings(orgId, scan?.id ?? null)
  const cancel = useCancelScan(orgId, scan?.revision ?? 0)
  return (
    <Sheet open={!!scan} onOpenChange={(o) => !o && onClose()}>
      <SheetContent side="end" className="w-full overflow-y-auto sm:max-w-xl" data-testid="scan-sheet">
        <SheetHeader>
          <SheetTitle>{t('security.scans.detail')}</SheetTitle>
          <SheetDescription>{scan && <Ltr>{scan.head_sha.slice(0, 12)}</Ltr>}</SheetDescription>
        </SheetHeader>
        {scan && (
          <div className="flex flex-col gap-4 px-4 pb-6">
            <p className="text-sm text-muted-foreground">
              {t('security.scans.config', { model: scan.config.model, mode: t(`security.budgetMode.${scan.config.budget_mode}`, { defaultValue: scan.config.budget_mode }), turns: scan.config.max_turns })}
            </p>
            {scan.tree_modified && <p className="text-sm">{t('security.scans.treeModifiedHelp')}</p>}
            {scanActive(scan) && (
              <div>
                <Button variant="outline" disabled={cancel.isPending} onClick={() => cancel.mutate({ orgId, scnId: scan.id }, { onError: (e) => toast.error(problemMessage(t, e)) })}>
                  {t('security.scans.cancel')}
                </Button>
              </div>
            )}
            {findings.isPending && <LoadingBlock rows={2} />}
            {findings.data && <SecurityFindings orgId={orgId} findings={findings.data} />}
          </div>
        )}
      </SheetContent>
    </Sheet>
  )
}
