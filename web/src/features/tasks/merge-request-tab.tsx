import { useTranslation } from 'react-i18next'
import { ExternalLinkIcon } from 'lucide-react'
import type { MergeRequestDto, RunDto } from '@/api/generated/model'
import { useMergeRequest } from '@/api/hooks/runDetail'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { Ltr } from '@/components/ltr'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { MergeRequestSecurity } from '@/features/security/mr-security'

/** One merge request: mode, state, heads, pipeline and the readiness checks. Shared by the task tab and the MR page. */
export function MergeRequestView({ mr }: { mr: MergeRequestDto }) {
  const { t } = useTranslation()
  const ready = mr.readiness
  return (
    <div className="flex flex-col gap-4">
      <Card>
        <CardHeader>
          <CardTitle className="flex flex-wrap items-center gap-2">
            <span dir="auto">{mr.title}</span>
            <Badge variant="outline" data-mr-mode={mr.mode}>
              {t(mr.mode === 'live' ? 'mr.mode.live' : 'mr.mode.dry_run')}
            </Badge>
            <Badge variant="outline" data-mr-state={mr.state}>
              {t(`mr.state.${mr.state}`)}
            </Badge>
            {mr.draft && <Badge variant="outline">{t('mr.draft')}</Badge>}
          </CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col gap-2 text-sm">
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1">
            <dt className="text-muted-foreground">{t('mr.branches')}</dt>
            <dd>
              <Ltr>{mr.source_branch}</Ltr> → <Ltr>{mr.target_branch}</Ltr>
            </dd>
            <dt className="text-muted-foreground">{t('mr.head')}</dt>
            <dd>
              <Ltr>{mr.current_head.slice(0, 12)}</Ltr>
            </dd>
            <dt className="text-muted-foreground">{t('mr.verified')}</dt>
            <dd>
              <Ltr>{mr.verified_head.slice(0, 12) || '-'}</Ltr>
            </dd>
            <dt className="text-muted-foreground">{t('mr.reviewed')}</dt>
            <dd>
              <Ltr>{mr.reviewed_head.slice(0, 12) || '-'}</Ltr>
            </dd>
            <dt className="text-muted-foreground">{t('mr.pipeline')}</dt>
            <dd>{mr.pipeline ? <Badge variant="outline">{t(`mr.pipelineStatus.${mr.pipeline.status}`, { defaultValue: mr.pipeline.status })}</Badge> : t('mr.noPipeline')}</dd>
          </dl>
          {mr.web_url && mr.mode === 'live' && (
            <a href={mr.web_url} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 text-muted-foreground underline-offset-4 hover:underline">
              <ExternalLinkIcon className="size-3.5" aria-hidden />
              {t('mr.open')}
            </a>
          )}
          {mr.mode === 'dry_run' && <p className="text-muted-foreground">{t('mr.simulated')}</p>}
        </CardContent>
      </Card>
      {ready && (
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              {t('mr.readiness')}
              <Badge variant={ready.ready ? 'default' : 'outline'} data-mr-ready={ready.ready}>
                {t(ready.ready ? 'mr.ready' : 'mr.notReady')}
              </Badge>
            </CardTitle>
          </CardHeader>
          <CardContent>
            <ul className="flex flex-col gap-1 text-sm" aria-label={t('mr.checksLabel')}>
              {ready.checks.map((c) => (
                <li key={c.code} className="flex flex-wrap items-center gap-2">
                  <Badge variant="outline" data-check={c.status}>
                    {t(`mr.check.${c.status}`)}
                  </Badge>
                  <span>{t(`mr.checkCodes.${c.code}`, { defaultValue: c.code })}</span>
                  {c.code === 'security_scan' ? (
                    c.detail && <span className="text-muted-foreground">{t(`security.checkDetail.${c.detail.replace(/\s+/g, '_')}`, { defaultValue: t(`security.scans.completion.${c.detail}`, { defaultValue: t('security.checkDetail.other') }) })}</span>
                  ) : (
                    c.detail && <span className="text-muted-foreground">{c.detail}</span>
                  )}
                </li>
              ))}
            </ul>
          </CardContent>
        </Card>
      )}
      {ready?.checks.some((c) => c.code === 'security_scan') && <MergeRequestSecurity runId={mr.run_id} />}
    </div>
  )
}

export function MergeRequestTab({ orgId, run }: { orgId: string; run: RunDto | undefined }) {
  const { t } = useTranslation()
  const mr = useMergeRequest(orgId, run?.merge_request_id)
  if (!run) return <p className="text-sm text-muted-foreground">{t('taskDetail.noRun')}</p>
  if (!run.merge_request_id) return <p className="text-sm text-muted-foreground">{t('mr.none')}</p>
  if (mr.isPending) return <LoadingBlock rows={2} />
  if (mr.isError) return <LoadFailed title={t('mr.loadFailed')} error={mr.error} onRetry={() => void mr.refetch()} />
  return <MergeRequestView mr={mr.data} />
}
