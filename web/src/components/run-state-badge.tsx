import type { ComponentType } from 'react'
import {
  BanIcon,
  CircleCheckIcon,
  CircleXIcon,
  ClipboardListIcon,
  CodeIcon,
  EyeIcon,
  GitMergeIcon,
  HourglassIcon,
  LayersIcon,
  LinkIcon,
  LockIcon,
  MessageCircleQuestionIcon,
  PauseIcon,
  ReplaceIcon,
  RotateCcwIcon,
  ScaleIcon,
  SearchIcon,
  ShieldCheckIcon,
  TimerIcon,
  TriangleAlertIcon,
  UploadIcon,
  WifiOffIcon,
  WrenchIcon,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'

type Icon = ComponentType<{ className?: string; 'aria-hidden'?: boolean }>
const NEUTRAL = 'bg-muted text-muted-foreground'
const PROGRESS = 'bg-sky-100 text-sky-900 dark:bg-sky-950 dark:text-sky-200'
const WAITING = 'bg-amber-100 text-amber-900 dark:bg-amber-950 dark:text-amber-200'
const GOOD = 'bg-emerald-100 text-emerald-900 dark:bg-emerald-950 dark:text-emerald-200'
const BAD = 'bg-red-100 text-red-900 dark:bg-red-950 dark:text-red-200'

/** Every run state has an icon and a word; colour only reinforces them. */
export const RUN_STATES: Record<string, { icon: Icon; tone: string }> = {
  discovered: { icon: SearchIcon, tone: NEUTRAL },
  gathering_context: { icon: LayersIcon, tone: PROGRESS },
  triaging: { icon: ScaleIcon, tone: PROGRESS },
  planned: { icon: ClipboardListIcon, tone: PROGRESS },
  queued: { icon: HourglassIcon, tone: GOOD },
  coding: { icon: CodeIcon, tone: PROGRESS },
  verifying: { icon: ShieldCheckIcon, tone: PROGRESS },
  publishing_draft: { icon: UploadIcon, tone: PROGRESS },
  reviewing: { icon: EyeIcon, tone: PROGRESS },
  awaiting_ci: { icon: TimerIcon, tone: WAITING },
  ready_for_merge: { icon: CircleCheckIcon, tone: GOOD },
  merged: { icon: GitMergeIcon, tone: GOOD },
  needs_input: { icon: MessageCircleQuestionIcon, tone: WAITING },
  waiting_dependency: { icon: LinkIcon, tone: WAITING },
  waiting_network: { icon: WifiOffIcon, tone: WAITING },
  fixing: { icon: WrenchIcon, tone: PROGRESS },
  failed: { icon: CircleXIcon, tone: BAD },
  cancelled: { icon: BanIcon, tone: NEUTRAL },
  superseded: { icon: ReplaceIcon, tone: NEUTRAL },
}

export function RunStateBadge({ state }: { state: string }) {
  const { t } = useTranslation()
  const s = RUN_STATES[state] ?? RUN_STATES.discovered
  const Icon = s.icon
  return (
    <Badge variant="outline" className={cn('border-transparent', s.tone)} data-run-state={state}>
      <Icon aria-hidden />
      {t(`run.state.${state}`, { defaultValue: state })}
    </Badge>
  )
}

export function RunHoldBadge({ code }: { code: string }) {
  const { t } = useTranslation()
  return (
    <Badge variant="outline" className={cn('border-transparent', WAITING)} data-run-hold={code}>
      <PauseIcon aria-hidden />
      {t(`run.hold.${code}`, { defaultValue: code })}
    </Badge>
  )
}
export function StaleBadge() {
  const { t } = useTranslation()
  return (
    <Badge variant="outline" className={cn('border-transparent', BAD)} data-run-stale>
      <TriangleAlertIcon aria-hidden />
      {t('run.stale')}
    </Badge>
  )
}
export function RiskBadge({ risk }: { risk: string | null | undefined }) {
  const { t } = useTranslation()
  if (!risk) return null
  const tone = risk === 'sensitive' ? BAD : risk === 'medium' ? WAITING : NEUTRAL
  const Icon = risk === 'sensitive' ? LockIcon : risk === 'medium' ? TriangleAlertIcon : RotateCcwIcon
  return (
    <Badge variant="outline" className={cn('border-transparent', tone)} data-risk={risk}>
      <Icon aria-hidden />
      {t(`triage.risk.${risk}`, { defaultValue: risk })}
    </Badge>
  )
}

interface RunLike {
  state: string
  hold?: { code: string } | null
  stale?: boolean
  eligibility?: string
}
/** State plus whatever holds or flags the run carries. */
export function RunBadges({ run }: { run: RunLike }) {
  return (
    <span className="inline-flex flex-wrap items-center gap-1.5">
      <RunStateBadge state={run.state} />
      {run.hold && <RunHoldBadge code={run.hold.code} />}
      {run.stale && <StaleBadge />}
    </span>
  )
}
