import { ExternalLinkIcon } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import type { InboxEvidenceDto } from '@/api/generated/model'
import { Ltr } from '@/components/ltr'

/** What the decision looked at. The excerpt is external text: shown as data, direction chosen by its own script. */
export function EvidenceList({ evidence }: { evidence: InboxEvidenceDto[] }) {
  const { t } = useTranslation()
  if (evidence.length === 0) return <p className="text-sm text-muted-foreground">{t('inbox.noEvidence')}</p>
  return (
    <ul className="flex flex-col gap-3" aria-label={t('inbox.evidence')}>
      {evidence.map((ev, i) => (
        <li key={`${ev.source_id}-${i}`} className="rounded-md border p-3 text-sm">
          <p className="mb-1 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
            <span>{t(`inbox.evidenceKind.${ev.kind}`, { defaultValue: ev.kind })}</span>
            <Ltr className="font-sans">{ev.label}</Ltr>
            {ev.url && (
              <a href={ev.url} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 underline underline-offset-4">
                <ExternalLinkIcon className="size-3" aria-hidden />
                {t('inbox.evidenceOpen')}
              </a>
            )}
          </p>
          <p dir="auto" className="whitespace-pre-wrap">
            {ev.excerpt}
          </p>
        </li>
      ))}
    </ul>
  )
}
