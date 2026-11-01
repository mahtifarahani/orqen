import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { FileTextIcon, RefreshCwIcon } from 'lucide-react'
import { toast } from 'sonner'
import type { ProjectDto } from '@/api/generated/model'
import { useArtifactText, useContractSnapshot, useContractSnapshots, useReviewContract, useStartContractImport } from '@/api/hooks/projects'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { Ltr } from '@/components/ltr'
import { useOperationRunner } from '@/components/use-operation-runner'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { conflictCurrent } from '@/lib/conflict'
import { formatDateTime, formatNumber } from '@/lib/format'
import { problemMessage } from '@/lib/problem-message'
import { useLocale } from '@/lib/use-locale'

export function ContractTab({ orgId, project, goTab }: { orgId: string; project: ProjectDto; goTab: (tab: string) => void }) {
  const { t } = useTranslation()
  const locale = useLocale()
  const snapshots = useContractSnapshots(orgId, project.id)
  const [chosen, setChosen] = useState<string>()
  const selectedId = chosen ?? project.latest_contract_snapshot_id ?? snapshots.data?.[0]?.id
  const snapshot = useContractSnapshot(orgId, project.id, selectedId)
  const [file, setFile] = useState<string>()
  const runner = useOperationRunner(orgId)
  const imp = useStartContractImport(runner.key)
  const review = useReviewContract(orgId, project.revision)
  const current = snapshot.data
  const filePath = file && current?.files.some((f) => f.path === file) ? file : current?.files[0]?.path
  const artifact = current?.files.find((f) => f.path === filePath)
  const text = useArtifactText(orgId, artifact?.artifact_id)
  const reviewed = !!selectedId && project.reviewed_contract_snapshot_id === selectedId
  const hasPrimary = project.mapping.repos.some((r) => r.role === 'primary')

  return (
    <div className="flex flex-col gap-4">
      <Card>
        <CardHeader className="flex-row flex-wrap items-center justify-between gap-2">
          <CardTitle>{t('contract.title')}</CardTitle>
          <div className="flex flex-wrap gap-2">
            <Button size="sm" variant="outline" disabled={!hasPrimary || imp.isPending || runner.running} onClick={() => imp.mutate({ orgId, prjId: project.id }, { onSuccess: runner.started, onError: (e) => toast.error(problemMessage(t, e)) })}>
              <RefreshCwIcon />
              {runner.running ? t('contract.importing') : snapshots.data?.length ? t('contract.reimport') : t('contract.import')}
            </Button>
            <Button
              size="sm"
              disabled={!selectedId || reviewed || review.isPending}
              onClick={() =>
                selectedId &&
                review.mutate(
                  { orgId, prjId: project.id, rcsId: selectedId },
                  {
                    onSuccess: () => toast.success(t('contract.reviewedToast')),
                    onError: (e) => toast.error(conflictCurrent(e) ? t('problems.revision_conflict') : problemMessage(t, e)),
                  },
                )
              }
            >
              {reviewed ? t('contract.reviewed') : t('contract.markReviewed')}
            </Button>
          </div>
        </CardHeader>
        <CardContent className="flex flex-col gap-4">
          <p className="max-w-prose text-sm text-muted-foreground">{t('contract.help')}</p>
          {!hasPrimary && <p className="text-sm text-muted-foreground">{t('contract.needsRepo')}</p>}
          {runner.failed && (
            <Alert variant="destructive" role="alert">
              <AlertDescription>{t('contract.importFailed')}</AlertDescription>
            </Alert>
          )}
          {snapshots.isPending && <LoadingBlock />}
          {snapshots.isError && <LoadFailed title={t('contract.loadFailed')} error={snapshots.error} onRetry={() => void snapshots.refetch()} />}
          {snapshots.isSuccess && snapshots.data.length === 0 && <p className="text-sm text-muted-foreground">{t('contract.empty')}</p>}
          {snapshots.isSuccess && snapshots.data.length > 0 && (
            <div className="flex flex-wrap items-center gap-3">
              <Select value={selectedId} onValueChange={(v) => setChosen(v)}>
                <SelectTrigger aria-label={t('contract.snapshot')} className="w-72">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {snapshots.data.map((s) => (
                    <SelectItem key={s.id} value={s.id}>
                      {formatDateTime(s.created_at, locale)} · {s.commit_sha.slice(0, 8)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              {reviewed ? <Badge>{t('contract.reviewed')}</Badge> : <Badge variant="outline">{t('contract.notReviewed')}</Badge>}
              {selectedId === project.latest_contract_snapshot_id && <Badge variant="secondary">{t('contract.latest')}</Badge>}
            </div>
          )}
          {current && (
            <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
              <dt className="text-muted-foreground">{t('contract.commit')}</dt>
              <dd><Ltr>{current.commit_sha}</Ltr></dd>
              <dt className="text-muted-foreground">{t('contract.ref')}</dt>
              <dd><Ltr>{current.ref_name}</Ltr></dd>
            </dl>
          )}
        </CardContent>
      </Card>

      {current && (
        <Card>
          <CardHeader>
            <CardTitle>{t('contract.files')}</CardTitle>
          </CardHeader>
          <CardContent className="grid gap-4 md:grid-cols-[16rem_1fr]">
            {current.files.length === 0 ? (
              <p className="text-sm text-muted-foreground">{t('contract.noFiles')}</p>
            ) : (
              <ul className="flex flex-col gap-1" aria-label={t('contract.files')}>
                {current.files.map((f) => (
                  <li key={f.path}>
                    <Button variant={f.path === filePath ? 'secondary' : 'ghost'} size="sm" className="w-full justify-start" aria-current={f.path === filePath ? 'true' : undefined} onClick={() => setFile(f.path)}>
                      <FileTextIcon />
                      <Ltr className="truncate">{f.path}</Ltr>
                    </Button>
                    <span className="ms-2 text-xs text-muted-foreground">
                      {formatNumber(f.size, locale)} B{f.truncated ? ` · ${t('contract.truncated')}` : ''}
                    </span>
                  </li>
                ))}
              </ul>
            )}
            <div className="min-w-0">
              {text.isPending && artifact && <LoadingBlock rows={2} />}
              {text.isError && <LoadFailed title={t('contract.fileFailed')} error={text.error} onRetry={() => void text.refetch()} />}
              {text.isSuccess && (
                // oxlint-disable-next-line jsx-a11y/no-noninteractive-tabindex -- a scrollable code region must be keyboard-reachable
                <pre dir="ltr" tabIndex={0} aria-label={filePath} className="max-h-96 overflow-auto rounded-lg border bg-muted/40 p-3 text-start font-mono text-xs whitespace-pre-wrap outline-none focus-visible:ring-2 focus-visible:ring-ring">
                  {text.data}
                </pre>
              )}
            </div>
          </CardContent>
        </Card>
      )}

      {current && (
        <Card>
          <CardHeader>
            <CardTitle>{t('commands.suggestions.title')}</CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-2 text-sm">
            {current.suggestions.length === 0 ? (
              <p className="text-muted-foreground">{t('commands.suggestions.none')}</p>
            ) : (
              <ul className="flex flex-col gap-1">
                {current.suggestions.map((s) => (
                  <li key={s.key} className="flex flex-wrap items-center gap-2">
                    <Badge variant="outline">{t(`commands.kind.${s.kind}`)}</Badge>
                    <Ltr className="text-xs">{s.argv.join(' ')}</Ltr>
                  </li>
                ))}
              </ul>
            )}
            <div>
              <Button size="sm" variant="outline" onClick={() => goTab('commands')}>
                {t('contract.toCommands')}
              </Button>
            </div>
          </CardContent>
        </Card>
      )}
    </div>
  )
}
