import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useLastRestore, useSupportBundle } from '@/api/hooks/backups'
import { Ltr } from '@/components/ltr'
import { LoadingBlock } from '@/components/query-state'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ApiProblem } from '@/api/problem'
import { formatDateTime } from '@/lib/format'
import { problemMessage } from '@/lib/problem-message'
import { useLocale } from '@/lib/use-locale'

/** Support bundle, the report of the last restore and how restoring works. */
export function OrgMaintenanceTab({ orgId }: { orgId: string }) {
  const { t } = useTranslation()
  const locale = useLocale()
  const bundle = useSupportBundle(orgId)
  const last = useLastRestore(orgId)
  const notFound = last.error instanceof ApiProblem && last.error.status === 404
  const download = async () => {
    const res = await bundle.refetch()
    if (res.isError || !res.data) return void toast.error(problemMessage(t, res.error))
    const blob = new Blob([JSON.stringify(res.data, null, 2)], { type: 'application/json' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `orqen-support-bundle-${res.data.generated_at.replace(/[^0-9TZ]/g, '')}.json`
    a.click()
    URL.revokeObjectURL(url)
  }
  const r = last.data
  return (
    <div className="flex max-w-4xl flex-col gap-8">
      <section className="flex flex-col gap-2" aria-label={t('maintenance.bundle.title')}>
        <h2 className="text-lg font-semibold">{t('maintenance.bundle.title')}</h2>
        <p className="text-sm text-muted-foreground">{t('maintenance.bundle.help')}</p>
        <div>
          <Button variant="outline" disabled={bundle.isFetching} onClick={() => void download()}>
            {t('maintenance.bundle.download')}
          </Button>
        </div>
      </section>

      <section className="flex flex-col gap-3" aria-label={t('maintenance.restore.title')}>
        <h2 className="text-lg font-semibold">{t('maintenance.restore.title')}</h2>
        <Alert>
          <AlertTitle>{t('maintenance.restore.howTitle')}</AlertTitle>
          <AlertDescription className="flex flex-col gap-2">
            <span>{t('maintenance.restore.how')}</span>
            <Ltr className="block overflow-x-auto rounded-md bg-muted px-2 py-1 text-xs">orqen restore --from &lt;backup-dir&gt; --dry-run</Ltr>
          </AlertDescription>
        </Alert>
        {last.isPending && <LoadingBlock rows={2} />}
        {notFound && <p className="text-sm text-muted-foreground">{t('maintenance.restore.none')}</p>}
        {last.isError && !notFound && <p role="alert" className="text-sm text-destructive">{problemMessage(t, last.error)}</p>}
        {r && (
          <div className="flex flex-col gap-3 text-sm" data-testid="restore-report">
            <p>
              {t('maintenance.restore.when', { when: formatDateTime(r.created_at, locale) })} · <Ltr className="break-all">{r.backup_name}</Ltr>
            </p>
            <p>{t('maintenance.restore.heldRuns', { count: r.held_runs })}</p>
            <p>{t('maintenance.restore.blobs', { count: r.blobs })}</p>
            <section className="flex flex-col gap-1">
              <h3 className="font-medium">{t('maintenance.restore.missing')}</h3>
              {r.missing_credentials.length === 0 ? (
                <p className="text-muted-foreground">{t('maintenance.restore.noMissing')}</p>
              ) : (
                <ul>
                  {r.missing_credentials.map((m) => (
                    <li key={`${m.kind}-${m.ref}`} className="flex flex-wrap items-center gap-2">
                      <Badge variant="outline">{t(`backups.credentialKind.${m.kind}`, { defaultValue: m.kind })}</Badge>
                      <Ltr>{m.owner}</Ltr>
                    </li>
                  ))}
                </ul>
              )}
            </section>
            {r.republish_profiles.length > 0 && (
              <section className="flex flex-col gap-1">
                <h3 className="font-medium">{t('maintenance.restore.republish')}</h3>
                <ul>
                  {r.republish_profiles.map((p) => (
                    <li key={`${p.org_id}-${p.name}`}><Ltr>{p.name}</Ltr></li>
                  ))}
                </ul>
              </section>
            )}
          </div>
        )}
      </section>
    </div>
  )
}
