import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ArrowLeftIcon, ArrowRightIcon } from 'lucide-react'
import type { SetupStepDto } from '@/api/generated/model'
import { ApiProblem } from '@/api/problem'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { problemMessage } from '@/lib/problem-message'
import { SkipDialog } from './skip-dialog'

export interface FooterActions {
  complete: (key: string) => Promise<unknown>
  skip: (key: string, reason: string | null) => Promise<unknown>
  reopen: (key: string) => Promise<unknown>
  finish: () => Promise<unknown>
}

/**
 * Back / Skip / Next for the current step. Next completes a todo step (a 409 lists what is missing and keeps
 * the user here), and just moves on from a step that is already done or skipped.
 */
export function StepFooter({
  step,
  first,
  last,
  busy,
  actions,
  onBack,
  onForward,
  onMissing,
}: {
  step: SetupStepDto
  first: boolean
  last: boolean
  busy: boolean
  actions: FooterActions
  onBack: () => void
  onForward: () => void
  /** Jump to the section that fixes a missing item (the panel is already on screen, so this only scrolls it into view). */
  onMissing?: () => void
}) {
  const { t } = useTranslation()
  const [skipping, setSkipping] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const missing = error instanceof ApiProblem && error.code === 'wizard_step_incomplete' ? (error.errors ?? []) : []
  const other = error && missing.length === 0 ? problemMessage(t, error) : null

  const next = async () => {
    setError(null)
    try {
      if (last) await actions.finish()
      else if (step.status === 'todo') await actions.complete(step.key)
      if (!last) onForward()
    } catch (e) {
      setError(e)
    }
  }
  const skip = async (reason: string | null) => {
    setError(null)
    try {
      await actions.skip(step.key, reason)
      setSkipping(false)
      if (!last) onForward()
    } catch (e) {
      setSkipping(false)
      setError(e)
    }
  }
  return (
    <div className="flex flex-col gap-3 border-t pt-4">
      {missing.length > 0 && (
        <Alert variant="destructive" role="alert" data-testid="missing-list">
          <AlertTitle>{t('wizard.incomplete')}</AlertTitle>
          <AlertDescription>
            <ul className="list-disc ps-5">
              {missing.map((m) => (
                <li key={m.field}>{t(`wizard.missing.${m.field}`, { defaultValue: m.message })}</li>
              ))}
            </ul>
            {onMissing && (
              <Button size="sm" variant="outline" className="mt-2" onClick={onMissing}>
                {t('wizard.goToSection')}
              </Button>
            )}
          </AlertDescription>
        </Alert>
      )}
      {other && (
        <Alert variant="destructive" role="alert">
          <AlertDescription>{other}</AlertDescription>
        </Alert>
      )}
      <div className="flex flex-wrap items-center gap-2">
        <Button variant="outline" onClick={onBack} disabled={first || busy}>
          <ArrowLeftIcon className="rtl:rotate-180" />
          {t('wizard.actions.back')}
        </Button>
        {step.status !== 'todo' && (
          <Button
            variant="ghost"
            disabled={busy}
            onClick={() => {
              setError(null)
              actions.reopen(step.key).catch(setError)
            }}
          >
            {t('wizard.actions.reopen')}
          </Button>
        )}
        <div className="ms-auto flex gap-2">
          {step.status === 'todo' && !last && (
            <Button variant="ghost" onClick={() => setSkipping(true)} disabled={busy}>
              {t('wizard.actions.skip')}
            </Button>
          )}
          <Button onClick={() => void next()} disabled={busy}>
            {last ? t('wizard.actions.finish') : step.status === 'todo' ? t('wizard.actions.next') : t('wizard.actions.continue')}
            {!last && <ArrowRightIcon className="rtl:rotate-180" />}
          </Button>
        </div>
      </div>
      {skipping && <SkipDialog step={step} pending={busy} onConfirm={(r) => void skip(r)} onCancel={() => setSkipping(false)} />}
    </div>
  )
}
