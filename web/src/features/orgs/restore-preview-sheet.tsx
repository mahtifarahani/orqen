import { useTranslation } from 'react-i18next'
import type { RestorePreviewDto } from '@/api/generated/model'
import { Ltr } from '@/components/ltr'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { formatNumber } from '@/lib/format'
import { useLocale } from '@/lib/use-locale'

/**
 * What restoring a backup would do, from the read-only verify. The web never restores: restoring replaces an install,
 * so it is a command the person runs with Orqen stopped.
 */
export function RestorePreviewSheet({ preview, dir, onClose }: { preview: RestorePreviewDto | null; dir: string; onClose: () => void }) {
  const { t } = useTranslation()
  const locale = useLocale()
  return (
    <Sheet open={!!preview} onOpenChange={(o) => !o && onClose()}>
      <SheetContent side="end" className="w-full overflow-y-auto sm:max-w-xl" data-testid="restore-preview">
        <SheetHeader>
          <SheetTitle>{t('backups.preview.title')}</SheetTitle>
          <SheetDescription>{preview && <Ltr className="break-all">{preview.name}</Ltr>}</SheetDescription>
        </SheetHeader>
        {preview && (
          <div className="flex flex-col gap-5 px-4 pb-6 text-sm">
            <div className="flex flex-wrap gap-2">
              <Badge variant={preview.integrity_ok ? 'default' : 'destructive'} data-integrity={preview.integrity_ok}>
                {t(preview.integrity_ok ? 'backups.preview.integrityOk' : 'backups.preview.integrityBad')}
              </Badge>
              <Badge variant={preview.schema_compatible ? 'outline' : 'destructive'}>{t(preview.schema_compatible ? 'backups.preview.schemaOk' : 'backups.preview.schemaBad')}</Badge>
              <Badge variant={preview.target_empty ? 'outline' : 'destructive'}>{t(preview.target_empty ? 'backups.preview.targetEmpty' : 'backups.preview.targetNotEmpty')}</Badge>
            </div>
            {preview.first_failed_file && (
              <p className="text-destructive">
                {t('backups.preview.firstFailed')} <Ltr>{preview.first_failed_file}</Ltr>
              </p>
            )}
            {!preview.target_empty && preview.target_non_empty.length > 0 && (
              <p className="text-muted-foreground">
                {t('backups.preview.nonEmpty')} <Ltr>{preview.target_non_empty.join(', ')}</Ltr>
              </p>
            )}
            <section className="flex flex-col gap-1">
              <h3 className="font-medium">{t('backups.preview.orgs')}</h3>
              <ul>
                {preview.orgs.map((o) => (
                  <li key={o.id}>
                    {o.name} <Ltr className="text-xs text-muted-foreground">{o.slug}</Ltr>
                  </li>
                ))}
              </ul>
            </section>
            <section className="flex flex-col gap-1">
              <h3 className="font-medium">{t('backups.preview.counts')}</h3>
              <ul className="grid grid-cols-[1fr_auto] gap-x-4">
                {Object.entries(preview.counts).map(([k, v]) => (
                  <li key={k} className="contents">
                    <Ltr className="text-xs">{k}</Ltr>
                    <span>{formatNumber(Number(v), locale)}</span>
                  </li>
                ))}
              </ul>
            </section>
            <section className="flex flex-col gap-1">
              <h3 className="font-medium">{t('backups.preview.afterRestore')}</h3>
              <p>{t('backups.preview.heldRuns', { count: preview.held_runs })}</p>
              {preview.missing_credentials.filter((c) => !c.present).length > 0 && (
                <>
                  <p>{t('backups.preview.missingCreds', { count: preview.missing_credentials.filter((c) => !c.present).length })}</p>
                  <ul className="text-xs text-muted-foreground">
                    {preview.missing_credentials.filter((c) => !c.present).map((c) => (
                      <li key={`${c.kind}-${c.ref}`}>
                        {t(`backups.credentialKind.${c.kind}`)} · <Ltr>{c.owner}</Ltr>
                      </li>
                    ))}
                  </ul>
                </>
              )}
              {preview.republish_profiles.length > 0 && <p>{t('backups.preview.republish', { count: preview.republish_profiles.length })}</p>}
            </section>
            <Alert>
              <AlertTitle>{t('backups.preview.cliTitle')}</AlertTitle>
              <AlertDescription className="flex flex-col gap-2">
                <span>{t('backups.preview.cliHelp')}</span>
                <Ltr className="block overflow-x-auto rounded-md bg-muted px-2 py-1 text-xs">orqen restore --from {dir}/{preview.name} --dry-run</Ltr>
                {!preview.can_restore && <span className="text-destructive">{t('backups.preview.cannot')}</span>}
              </AlertDescription>
            </Alert>
          </div>
        )}
      </SheetContent>
    </Sheet>
  )
}
