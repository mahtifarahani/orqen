import { useTranslation } from 'react-i18next'
import { useProposals } from '@/api/hooks/knowledge'
import { Ltr } from '@/components/ltr'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { Badge } from '@/components/ui/badge'
import { formatDateTime } from '@/lib/format'
import { useLocale } from '@/lib/use-locale'

/** Proposals: what an author (deterministic or roles) drafted, with the translated reasons a draft was not promoted. */
export function ProposalsSection({ orgId, onOpen }: { orgId: string; onOpen: (recordId: string) => void }) {
  const { t } = useTranslation()
  const locale = useLocale()
  const proposals = useProposals(orgId)
  const items = proposals.data?.items ?? []
  return (
    <section aria-label={t('knowledgePage.proposalsSection.title')} className="flex flex-col gap-3">
      <h2 className="text-lg font-semibold">{t('knowledgePage.proposalsSection.title')}</h2>
      {proposals.isPending && <LoadingBlock rows={2} />}
      {proposals.isError && <LoadFailed title={t('knowledgePage.proposalsSection.title')} error={proposals.error} onRetry={() => void proposals.refetch()} />}
      {proposals.isSuccess && items.length === 0 && <p className="text-sm text-muted-foreground">{t('knowledgePage.proposalsSection.none')}</p>}
      <ul className="flex flex-col gap-2">
        {items.slice(0, 10).map((p) => (
          <li key={p.id} className="flex flex-col gap-2 rounded-md border p-3 text-sm">
            <div className="flex flex-wrap items-center gap-2">
              <Badge variant="outline">{t(`knowledgePage.proposalsSection.trigger.${p.trigger}`, { defaultValue: p.trigger })}</Badge>
              <Badge variant="secondary">{t(`knowledgePage.proposalsSection.state.${p.state}`, { defaultValue: p.state })}</Badge>
              <span className="text-muted-foreground">{formatDateTime(p.created_at, locale)}</span>
            </div>
            <ul className="flex flex-col gap-1">
              {p.items.map((i) => (
                <li key={`${i.key}-${i.version_id}`} className="flex flex-wrap items-center gap-2">
                  <button type="button" className="text-start underline-offset-2 hover:underline disabled:no-underline" disabled={!i.record_id} onClick={() => onOpen(i.record_id)}>
                    <Ltr>{i.key}</Ltr>
                  </button>
                  <Badge variant="outline">{t(`knowledgePage.proposalsSection.outcome.${i.outcome}`, { defaultValue: i.outcome })}</Badge>
                  {i.reasons.map((r) => (
                    <span key={r} className="text-xs text-muted-foreground">{t(`knowledgePage.neverAuto.${r}`, { defaultValue: t(`problems.${r}`, { defaultValue: r }) })}</span>
                  ))}
                </li>
              ))}
            </ul>
          </li>
        ))}
      </ul>
    </section>
  )
}
