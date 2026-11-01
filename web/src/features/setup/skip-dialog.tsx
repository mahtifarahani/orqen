import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { SetupStepDto } from '@/api/generated/model'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Textarea } from '@/components/ui/textarea'
import { Label } from '@/components/ui/label'

/** Shown before a step is skipped: what will not work, and what is still missing right now. Nothing is skipped until confirmed. */
export function SkipDialog({ step, pending, onConfirm, onCancel }: { step: SetupStepDto; pending: boolean; onConfirm: (reason: string | null) => void; onCancel: () => void }) {
  const { t } = useTranslation()
  const [reason, setReason] = useState('')
  return (
    <Dialog open onOpenChange={(o) => !o && onCancel()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('wizard.skipDialog.title', { step: t(`wizard.steps.${step.key}.title`) })}</DialogTitle>
          <DialogDescription>{t('wizard.skipDialog.body')}</DialogDescription>
        </DialogHeader>
        <section aria-label={t('wizard.skipDialog.consequences')} className="flex flex-col gap-2 text-sm">
          <h3 className="font-medium">{t('wizard.skipDialog.consequences')}</h3>
          {step.consequences.length === 0 ? (
            <p className="text-muted-foreground">{t('wizard.skipDialog.none')}</p>
          ) : (
            <ul className="list-disc ps-5">
              {step.consequences.map((c) => (
                <li key={c}>{t(`wizard.consequence.${c}`, { defaultValue: c })}</li>
              ))}
            </ul>
          )}
        </section>
        {step.missing.length > 0 && (
          <section aria-label={t('wizard.skipDialog.missing')} className="flex flex-col gap-2 text-sm">
            <h3 className="font-medium">{t('wizard.skipDialog.missing')}</h3>
            <ul className="list-disc ps-5">
              {step.missing.map((m) => (
                <li key={m.code}>{t(`wizard.missing.${m.code}`, { defaultValue: m.detail })}</li>
              ))}
            </ul>
          </section>
        )}
        <p className="text-xs text-muted-foreground">{t('wizard.skipDialog.readiness')}</p>
        <div className="flex flex-col gap-2">
          <Label htmlFor="skip-reason">{t('wizard.skipDialog.reason')}</Label>
          <Textarea id="skip-reason" rows={2} maxLength={500} value={reason} onChange={(e) => setReason(e.target.value)} />
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onCancel}>
            {t('common.cancel')}
          </Button>
          <Button variant="destructive" disabled={pending} onClick={() => onConfirm(reason.trim() || null)}>
            {t('wizard.skipDialog.confirm')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
