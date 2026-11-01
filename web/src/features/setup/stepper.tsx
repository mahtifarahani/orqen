import { CheckIcon, ChevronRightIcon, CircleDashedIcon, MinusIcon, TriangleAlertIcon } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import type { SetupStepDto } from '@/api/generated/model'
import { cn } from '@/lib/utils'

/** The wizard's visible state of a step: a done step whose criteria fail now needs attention. */
function stepState(s: SetupStepDto): 'attention' | 'done' | 'skipped' | 'todo' {
  if (s.attention) return 'attention'
  return s.status
}

const ICON = { done: CheckIcon, skipped: MinusIcon, attention: TriangleAlertIcon, todo: CircleDashedIcon } as const

/** Ordered list of steps. Status is text plus an icon, never colour alone; the current step carries aria-current. */
export function Stepper({ steps, current, onSelect }: { steps: SetupStepDto[]; current: string; onSelect: (key: string) => void }) {
  const { t } = useTranslation()
  return (
    <ol aria-label={t('wizard.stepsLabel')} className="flex flex-col gap-1" data-testid="stepper">
      {steps.map((s, i) => {
        const state = stepState(s)
        const Icon = ICON[state]
        const isCurrent = s.key === current
        return (
          <li key={s.key}>
            <button
              type="button"
              aria-current={isCurrent ? 'step' : undefined}
              onClick={() => onSelect(s.key)}
              className={cn(
                'flex w-full items-center gap-3 rounded-md px-3 py-2 text-start text-sm outline-none hover:bg-accent focus-visible:ring-2 focus-visible:ring-ring',
                isCurrent && 'bg-accent font-medium',
              )}
            >
              <span className="flex size-6 shrink-0 items-center justify-center rounded-full border text-xs tabular-nums" aria-hidden>
                {i + 1}
              </span>
              <span className="flex min-w-0 flex-1 flex-col">
                <span>{t(`wizard.steps.${s.key}.title`)}</span>
                <span className="flex items-center gap-1 text-xs text-muted-foreground" data-status={state}>
                  <Icon className="size-3.5" aria-hidden />
                  {t(`wizard.status.${state}`)}
                </span>
              </span>
              <ChevronRightIcon className="size-4 shrink-0 text-muted-foreground rtl:rotate-180" aria-hidden />
            </button>
          </li>
        )
      })}
    </ol>
  )
}
