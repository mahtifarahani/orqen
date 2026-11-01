import { useState, type FormEvent, type RefObject } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useDeleteCredential, usePutCredential, type Connection } from '@/api/hooks/connections'
import { Field } from '@/components/field'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { formatDateTime } from '@/lib/format'
import { problemMessage } from '@/lib/problem-message'
import { useLocale } from '@/lib/use-locale'

/** Write-only: the secret is never read back, and the input is cleared as soon as it is saved. */
export function CredentialDialog({
  orgId,
  connection,
  open,
  onOpenChange,
  returnFocusRef,
}: {
  orgId: string
  connection: Connection
  open: boolean
  onOpenChange: (open: boolean) => void
  returnFocusRef?: RefObject<HTMLElement | null>
}) {
  const { t } = useTranslation()
  const locale = useLocale()
  const [secret, setSecret] = useState('')
  const [confirmOpen, setConfirmOpen] = useState(false)
  const put = usePutCredential(orgId, connection.revision)
  const del = useDeleteCredential(orgId, connection.revision)
  const isSet = connection.credential.state === 'set'

  const save = (e: FormEvent) => {
    e.preventDefault()
    if (!secret) return
    put.mutate(
      { orgId, conId: connection.id, data: { secret } },
      {
        onSuccess: () => {
          setSecret('')
          toast.success(t('connections.credentialDialog.saved'))
        },
        onSettled: () => setSecret(''),
      },
    )
  }

  const close = (o: boolean) => {
    if (!o) {
      setSecret('')
      put.reset()
      del.reset()
    }
    onOpenChange(o)
  }

  return (
    <>
      <Dialog open={open} onOpenChange={close}>
        <DialogContent
          onCloseAutoFocus={(e) => {
            if (returnFocusRef?.current) {
              e.preventDefault()
              returnFocusRef.current.focus()
            }
          }}
        >
          <DialogHeader>
            <DialogTitle>
              {t('connections.credentialDialog.title')} · {connection.name}
            </DialogTitle>
            <DialogDescription>{t('connections.credentialDialog.description')}</DialogDescription>
          </DialogHeader>
          <p className="text-sm" role="status">
            {isSet
              ? t('connections.credentialDialog.setAt', { date: formatDateTime(connection.credential.updated_at, locale) })
              : t('connections.credentialDialog.noneYet')}
          </p>
          <form onSubmit={save} className="flex flex-col gap-4">
            <Field id="cred-secret" label={t('connections.credentialDialog.secret')} hint={t('connections.credentialDialog.secretHint')}>
              {(p) => (
                <Input
                  {...p}
                  dir="ltr"
                  type="password"
                  autoComplete="off"
                  spellCheck={false}
                  value={secret}
                  onChange={(e) => setSecret(e.target.value)}
                />
              )}
            </Field>
            {(put.isError || del.isError) && (
              <Alert variant="destructive" role="alert">
                <AlertDescription>{problemMessage(t, put.error ?? del.error)}</AlertDescription>
              </Alert>
            )}
            <DialogFooter>
              {isSet && (
                <Button type="button" variant="outline" className="me-auto" onClick={() => setConfirmOpen(true)}>
                  {t('connections.credentialDialog.remove')}
                </Button>
              )}
              <Button type="button" variant="outline" onClick={() => close(false)}>
                {t('common.close')}
              </Button>
              <Button type="submit" disabled={put.isPending || !secret}>
                {t('connections.credentialDialog.save')}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      <Dialog open={confirmOpen} onOpenChange={setConfirmOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('connections.credentialDialog.confirmTitle')}</DialogTitle>
            <DialogDescription>{t('connections.credentialDialog.confirmBody')}</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setConfirmOpen(false)}>
              {t('common.cancel')}
            </Button>
            <Button
              type="button"
              variant="destructive"
              disabled={del.isPending}
              onClick={() =>
                del.mutate(
                  { orgId, conId: connection.id },
                  {
                    onSuccess: () => {
                      toast.success(t('connections.credentialDialog.removed'))
                      setConfirmOpen(false)
                    },
                  },
                )
              }
            >
              {t('connections.credentialDialog.confirm')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}
