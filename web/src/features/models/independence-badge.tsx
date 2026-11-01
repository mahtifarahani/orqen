import { CircleCheckIcon, CircleHelpIcon, TriangleAlertIcon, UserRoundXIcon } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import type { Independence } from '@/api/generated/model'
import { Badge } from '@/components/ui/badge'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { cn } from '@/lib/utils'

const LOOK = {
  independent: { icon: CircleCheckIcon, tone: 'bg-emerald-100 text-emerald-900 dark:bg-emerald-950 dark:text-emerald-200' },
  same_model: { icon: TriangleAlertIcon, tone: 'bg-amber-100 text-amber-900 dark:bg-amber-950 dark:text-amber-200' },
  unknown: { icon: CircleHelpIcon, tone: 'bg-muted text-muted-foreground' },
  reviewer_disabled: { icon: UserRoundXIcon, tone: 'bg-muted text-muted-foreground' },
} as const

/** Reviewer independence. The words carry the meaning; the icon and tint only support them. */
export function IndependenceBadge({ independence }: { independence: Independence }) {
  const { t } = useTranslation()
  const look = LOOK[independence.status] ?? LOOK.unknown
  const Icon = look.icon
  const detail = [...independence.shared, ...independence.unmatched]
  const badge = (
    <Badge variant="outline" className={cn('border-transparent', look.tone)} data-independence={independence.status}>
      <Icon aria-hidden />
      {t(`independence.${independence.status}`)}
    </Badge>
  )
  if (detail.length === 0) return badge
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <button type="button" className="inline-flex rounded-md outline-none focus-visible:ring-2 focus-visible:ring-ring">
          {badge}
        </button>
      </TooltipTrigger>
      <TooltipContent>
        <span dir="ltr">{detail.join(', ')}</span>
      </TooltipContent>
    </Tooltip>
  )
}
