import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Field } from '@/components/field'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import type { AcceptExtensionLicenseRequest } from '@/api/generated/model'
import type { ExtensionDetail } from '@/api/hooks/extensions'
import { Ltr } from '@/components/ltr'
import { problemMessage } from '@/lib/problem-message'
import { useLocale } from '@/lib/use-locale'

const BASES = ['noncommercial_use', 'commercial_license_obtained', 'other_compatible'] as const

/**
 * The license gate of an extension such as GitNexus. Acceptance needs the person to read the Orqen-written notice
 * (its digest is sent back), to pick the basis they rely on, to write a note and to type the extension id.
 */
export function LicenseDialog({
  detail,
  open,
  onOpenChange,
  pending,
  error,
  onAccept,
}: {
  detail: ExtensionDetail
  open: boolean
  onOpenChange: (open: boolean) => void
  pending: boolean
  error: unknown
  onAccept: (body: AcceptExtensionLicenseRequest) => void
}) {
  const { t } = useTranslation()
  const locale = useLocale()
  const id = detail.manifest.id
  const notice = detail.license_notices.find((n) => n.locale === locale) ?? detail.license_notices[0]
  const [basis, setBasis] = useState<(typeof BASES)[number] | ''>('')
  const [note, setNote] = useState('')
  const [phrase, setPhrase] = useState('')
  const ready = !!notice && !!basis && note.trim().length > 0 && phrase === id

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t('extensions.license.title', { name: t(`extensions.names.${id}`, { defaultValue: id }) })}</DialogTitle>
          <DialogDescription>
            {t('extensions.license.spdx')} <Ltr>{detail.manifest.license.spdx}</Ltr>
          </DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault()
            if (!ready || !basis || !notice) return
            onAccept({ basis, note: note.trim(), confirm_phrase: phrase, notice_sha256: notice.sha256 })
          }}
        >
          <blockquote className="rounded-md border bg-muted/40 p-3 text-sm" data-testid="license-notice">
            {notice?.text}
          </blockquote>
          <fieldset className="flex flex-col gap-2">
            <legend className="mb-1 text-sm font-medium">{t('extensions.license.basis')}</legend>
            {BASES.map((b) => (
              <label key={b} className="flex items-start gap-2 text-sm">
                <input type="radio" name="license-basis" className="mt-1" checked={basis === b} onChange={() => setBasis(b)} />
                <span>{t(`extensions.license.bases.${b}`)}</span>
              </label>
            ))}
          </fieldset>
          <Field id="license-note" label={t('extensions.license.note')} hint={t('extensions.license.noteHint')}>
            {(f) => <Textarea {...f} value={note} onChange={(e) => setNote(e.target.value)} rows={3} />}
          </Field>
          <Field id="license-phrase" label={t('extensions.license.phrase', { id })}>
            {(f) => <Input {...f} dir="ltr" autoComplete="off" value={phrase} onChange={(e) => setPhrase(e.target.value)} />}
          </Field>
          {!!error && (
            <Alert variant="destructive" role="alert">
              <AlertDescription>{problemMessage(t, error)}</AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" disabled={!ready || pending}>
              {t('extensions.license.accept')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
