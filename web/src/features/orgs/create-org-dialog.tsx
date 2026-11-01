import { useState, type FormEvent, type RefObject } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router'
import { toast } from 'sonner'
import { useCreateOrg } from '@/api/hooks/orgs'
import { newIdempotencyKey } from '@/api/idempotency'
import { ApiProblem, ok } from '@/api/problem'
import { Field } from '@/components/field'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { isLocale, type Locale } from '@/i18n/direction'
import { setLastOrg } from '@/lib/last-org'
import { problemMessage } from '@/lib/problem-message'
import { useLocale } from '@/lib/use-locale'

/** The create-organization fields. `onCreated` replaces the default redirect to the new Overview (the setup wizard uses it). */
export function CreateOrgForm({ onDone, onCreated, submitLabel }: { onDone?: () => void; onCreated?: (org: { id: string }) => void; submitLabel?: string }) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const uiLocale = useLocale()
  // One idempotency key per dialog open: a retry replays instead of creating a second org.
  const [key] = useState(newIdempotencyKey)
  const create = useCreateOrg(key)
  const [slug, setSlug] = useState('')
  const [name, setName] = useState('')
  const [locale, setLocale] = useState<Locale>(uiLocale)
  const [timezone, setTimezone] = useState(() => Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC')

  const fieldErrors = create.error instanceof ApiProblem ? create.error.fieldErrors() : {}
  const generalError =
    create.isError && Object.keys(fieldErrors).length === 0 ? problemMessage(t, create.error) : undefined

  const submit = (e: FormEvent) => {
    e.preventDefault()
    create.mutate(
      { data: { slug: slug.trim(), name: name.trim(), locale, timezone: timezone.trim() } },
      {
        onSuccess: (res) => {
          const org = ok(res)
          toast.success(t('orgs.create.created'))
          setLastOrg(org.id)
          onDone?.()
          if (onCreated) onCreated(org)
          else navigate(`/orgs/${org.id}/overview`)
        },
      },
    )
  }

  return (
    <form onSubmit={submit} className="flex flex-col gap-4">
      <Field id="org-slug" label={t('orgs.fields.slug')} hint={t('orgs.hints.slug')} error={fieldErrors.slug}>
        {(p) => (
          <Input {...p} dir="ltr" required value={slug} onChange={(e) => setSlug(e.target.value)} autoComplete="off" />
        )}
      </Field>
      <Field id="org-name" label={t('orgs.fields.name')} error={fieldErrors.name}>
        {(p) => <Input {...p} required value={name} onChange={(e) => setName(e.target.value)} autoComplete="off" />}
      </Field>
      <Field id="org-locale" label={t('orgs.fields.locale')} error={fieldErrors.locale}>
        {(p) => (
          <Select value={locale} onValueChange={(v) => isLocale(v) && setLocale(v)}>
            <SelectTrigger {...p} className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="fa">{t('locale.fa')}</SelectItem>
              <SelectItem value="en">{t('locale.en')}</SelectItem>
            </SelectContent>
          </Select>
        )}
      </Field>
      <Field id="org-timezone" label={t('orgs.fields.timezone')} hint={t('orgs.hints.timezone')} error={fieldErrors.timezone}>
        {(p) => <Input {...p} dir="ltr" required value={timezone} onChange={(e) => setTimezone(e.target.value)} />}
      </Field>
      {generalError && (
        <Alert variant="destructive" role="alert">
          <AlertDescription>{generalError}</AlertDescription>
        </Alert>
      )}
      <DialogFooter>
        {onDone && (
          <Button type="button" variant="outline" onClick={onDone}>
            {t('common.cancel')}
          </Button>
        )}
        <Button type="submit" disabled={create.isPending}>
          {submitLabel ?? t('orgs.create.submit')}
        </Button>
      </DialogFooter>
    </form>
  )
}

export function CreateOrgDialog({
  open,
  onOpenChange,
  returnFocusRef,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Element that opened the dialog; receives focus when it closes. */
  returnFocusRef?: RefObject<HTMLElement | null>
}) {
  const { t } = useTranslation()
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        onCloseAutoFocus={(e) => {
          if (returnFocusRef?.current) {
            e.preventDefault()
            returnFocusRef.current.focus()
          }
        }}
      >
        <DialogHeader>
          <DialogTitle>{t('orgs.create.title')}</DialogTitle>
          <DialogDescription>{t('orgs.create.description')}</DialogDescription>
        </DialogHeader>
        {/* mounted only while open, so the idempotency key is created once per open */}
        <CreateOrgForm onDone={() => onOpenChange(false)} />
      </DialogContent>
    </Dialog>
  )
}
