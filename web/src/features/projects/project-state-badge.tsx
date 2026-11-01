import { CircleCheckIcon, CircleDashedIcon, PlayIcon, TriangleAlertIcon } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'

const STATES = {
  draft: { icon: CircleDashedIcon, tone: 'bg-muted text-muted-foreground' },
  ready: { icon: CircleCheckIcon, tone: 'bg-emerald-100 text-emerald-900 dark:bg-emerald-950 dark:text-emerald-200' },
  enabled: { icon: PlayIcon, tone: 'bg-sky-100 text-sky-900 dark:bg-sky-950 dark:text-sky-200' },
  enabled_blocked: { icon: TriangleAlertIcon, tone: 'bg-amber-100 text-amber-900 dark:bg-amber-950 dark:text-amber-200' },
} as const

/** A project's derived state. Text and an icon carry the meaning, never the colour alone. */
export function ProjectStateBadge({ state }: { state: string }) {
  const { t } = useTranslation()
  const s = STATES[state as keyof typeof STATES] ?? STATES.draft
  const Icon = s.icon
  return (
    <Badge variant="outline" className={cn('border-transparent', s.tone)} data-state={state}>
      <Icon aria-hidden />
      {t(`projects.state.${state}`, { defaultValue: state })}
    </Badge>
  )
}
