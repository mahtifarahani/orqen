import { useTranslation } from 'react-i18next'
import type { RunDto } from '@/api/generated/model'
import { useRunDiff } from '@/api/hooks/runDetail'
import { Ltr } from '@/components/ltr'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'

/** The files the run changed between the base and the head, with test-weakening signals. */
export function DiffTab({ orgId, run }: { orgId: string; run: RunDto | undefined }) {
  const { t } = useTranslation()
  const diff = useRunDiff(orgId, run?.id, !!run?.heads?.head)
  if (!run) return <p className="text-sm text-muted-foreground">{t('taskDetail.noRun')}</p>
  if (!run.heads?.head) return <p className="text-sm text-muted-foreground">{t('diff.none')}</p>
  if (diff.isPending) return <LoadingBlock rows={2} />
  if (diff.isError) return <LoadFailed title={t('diff.loadFailed')} error={diff.error} onRetry={() => void diff.refetch()} />
  const d = diff.data
  return (
    <div className="flex flex-col gap-4">
      <p className="text-sm text-muted-foreground">
        <Ltr>{d.base_sha.slice(0, 12)}</Ltr> → <Ltr>{d.head_sha.slice(0, 12)}</Ltr>
      </p>
      {d.weakening.suspected && (
        <Alert variant="destructive">
          <AlertTitle>{t('verification.weakening')}</AlertTitle>
          <AlertDescription>
            <ul className="list-disc ps-5">
              {d.weakening.signals.map((s, i) => (
                <li key={i}>
                  {t(`verification.signals.${s.code}`, { defaultValue: s.code })} <Ltr>{s.path}</Ltr>
                </li>
              ))}
            </ul>
          </AlertDescription>
        </Alert>
      )}
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{t('diff.file')}</TableHead>
            <TableHead>{t('diff.status')}</TableHead>
            <TableHead>+</TableHead>
            <TableHead>-</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {d.files.map((f) => (
            <TableRow key={f.path}>
              <TableCell className="break-all">
                <Ltr>{f.path}</Ltr>
              </TableCell>
              <TableCell>{t(`diff.kind.${f.status}`, { defaultValue: f.status })}</TableCell>
              <TableCell className="text-emerald-700 dark:text-emerald-300">
                <Ltr>{f.additions}</Ltr>
              </TableCell>
              <TableCell className="text-red-700 dark:text-red-300">
                <Ltr>{f.deletions}</Ltr>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  )
}
