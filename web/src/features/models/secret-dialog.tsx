import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import type { CatherdSecretDto } from '@/api/generated/model'
import { usePutCatherdSecretValue } from '@/api/hooks/catherd'
import { ApiProblem } from '@/api/problem'
import { Field } from '@/components/field'
import { Ltr } from '@/components/ltr'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { problemMessage } from '@/lib/problem-message'

/**
 * Set or replace one catherd key. Write-only: the typed value lives in this component's state until the request
 * settles, is sent once in the PUT body, and is cleared on success, failure and close. Nothing reads it back.
 */
export function SecretDialog({ orgId, secret, open, onOpenChange }: { orgId: string; secret: CatherdSecretDto; open: boolean; onOpenChange: (open: boolean) => void }) {
  const { t } = useTranslation()
  const [value, setValue] = useState('')
  const put = usePutCatherdSecretValue(orgId, secret.revision)
  const isSet = secret.state === 'set'
  const label = t(`secrets.kind.${secret.kind}`)

  const close = (o: boolean) => {
    if (!o) {
      setValue('')
      put.reset()
    }
    onOpenChange(o)
  }
  const save = (e: FormEvent) => {
    e.preventDefault()
    if (!value) return
    put.mutate(
      { orgId, kind: secret.kind, data: { secret: value } },
      {
        onSuccess: () => {
          setValue('')
          toast.success(t('models.keys.dialog.saved'))
          onOpenChange(false)
        },
        onSettled: () => setValue(''),
      },
    )
  }
  const field = put.error instanceof ApiProblem && put.error.code === 'secret_invalid' ? put.error.fieldErrors().secret : undefined

  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {isSet ? t('models.keys.dialog.replaceTitle', { name: label }) : t('models.keys.dialog.setTitle', { name: label })}
          </DialogTitle>
          <DialogDescription>{t('models.keys.dialog.description')}</DialogDescription>
        </DialogHeader>
        <p className="text-sm text-muted-foreground">
          {t('models.keys.dialog.envHint')} <Ltr>{secret.env_name}</Ltr>
        </p>
        <form onSubmit={save} className="flex flex-col gap-4">
          <Field id={`secret-${secret.kind}`} label={t('models.keys.dialog.value')} hint={t('models.keys.dialog.valueHint')} error={field ? t('models.keys.dialog.invalid') : undefined}>
            {(p) => <Input {...p} dir="ltr" type="password" autoComplete="off" spellCheck={false} value={value} onChange={(e) => setValue(e.target.value)} />}
          </Field>
          {put.isError && !field && (
            <Alert variant="destructive" role="alert">
              <AlertDescription>{problemMessage(t, put.error)}</AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => close(false)}>
              {t('common.close')}
            </Button>
            <Button type="submit" disabled={put.isPending || !value}>
              {t('models.keys.dialog.save')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
