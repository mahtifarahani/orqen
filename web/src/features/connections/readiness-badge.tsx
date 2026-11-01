import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import type { Readiness } from '@/api/generated/model'

export function ReadinessBadge({ readiness }: { readiness: Readiness }) {
  const { t } = useTranslation()
  if (readiness.unattended_ready) return <Badge>{t('readiness.ready')}</Badge>
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <button type="button" className="inline-flex rounded-md outline-none focus-visible:ring-2 focus-visible:ring-ring">
          <Badge variant="outline" className="h-auto whitespace-normal border-amber-500/60 py-0.5 text-start text-amber-800 dark:text-amber-300">
            {t('readiness.notReady')}
          </Badge>
        </button>
      </TooltipTrigger>
      <TooltipContent>
        <ul className="list-disc ps-4">
          {readiness.reasons.map((r) => (
            <li key={r}>{t(`readiness.reasons.${r}`, { defaultValue: r })}</li>
          ))}
        </ul>
      </TooltipContent>
    </Tooltip>
  )
}
