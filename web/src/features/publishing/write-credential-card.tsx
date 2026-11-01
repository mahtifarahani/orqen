import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useCheckWriteCredentialRun, useDeleteWriteCredentialSecret, usePutWriteCredentialSecret, useWriteCredential } from '@/api/hooks/writeCredential'
import type { Connection } from '@/api/hooks/connections'
import { useOperationRunner } from '@/components/use-operation-runner'
import { Field } from '@/components/field'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { formatDateTime } from '@/lib/format'
import { problemMessage } from '@/lib/problem-message'
import { useLocale } from '@/lib/use-locale'

/**
 * The separate write credential of a GitLab connection. Write-only: the secret is never read back and is cleared as
 * soon as it is saved. Orqen never creates it; the user enters it.
 */
export function WriteCredentialCard({ orgId, connection }: { orgId: string; connection: Connection }) {
  const { t } = useTranslation()
  const locale = useLocale()
  const cred = useWriteCredential(orgId, connection.id)
  const rev = cred.data?.revision ?? 0
  const put = usePutWriteCredentialSecret(orgId, connection.id, rev)
  const del = useDeleteWriteCredentialSecret(orgId, connection.id, rev)
  const runner = useOperationRunner(orgId)
  const check = useCheckWriteCredentialRun(runner.key)
  const [secret, setSecret] = useState('')
  const isSet = cred.data?.state === 'set'
  const last = cred.data?.last_check

  const save = (e: FormEvent) => {
    e.preventDefault()
    if (!secret) return
    put.mutate(
      { orgId, conId: connection.id, data: { secret } },
      { onSuccess: () => toast.success(t('writeCredential.saved')), onSettled: () => setSecret('') },
    )
  }
  return (
    <section className="flex flex-col gap-3 rounded-lg border p-4" aria-label={`${t('writeCredential.title')} ${connection.name}`}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="font-medium">{connection.name}</h3>
        <Badge variant="outline" data-write-credential={cred.data?.state ?? 'none'}>
          {t(isSet ? 'writeCredential.set' : 'writeCredential.none')}
        </Badge>
      </div>
      <p className="text-sm text-muted-foreground">{t('writeCredential.help')}</p>
      {last && (
        <p className="text-sm" role="status">
          {t(`writeCredential.check.${last.status}`)} · {formatDateTime(last.checked_at, locale)}
          {last.detail && <span className="text-muted-foreground"> · {last.detail}</span>}
        </p>
      )}
      <form onSubmit={save} className="flex flex-col gap-3">
        <Field id={`wcred-${connection.id}`} label={t('writeCredential.secret')} hint={t('writeCredential.secretHint')}>
          {(f) => <Input {...f} dir="ltr" type="password" autoComplete="off" value={secret} onChange={(e) => setSecret(e.target.value)} />}
        </Field>
        {(put.isError || del.isError || check.isError) && (
          <Alert variant="destructive" role="alert">
            <AlertDescription>{problemMessage(t, put.error ?? del.error ?? check.error)}</AlertDescription>
          </Alert>
        )}
        <div className="flex flex-wrap gap-2">
          <Button type="submit" disabled={!secret || put.isPending}>
            {t(isSet ? 'writeCredential.replace' : 'writeCredential.save')}
          </Button>
          <Button
            type="button"
            variant="outline"
            disabled={!isSet || check.isPending || runner.running}
            onClick={() => check.mutate({ orgId, conId: connection.id }, { onSuccess: runner.started })}
          >
            {t(runner.running ? 'writeCredential.checking' : 'writeCredential.checkNow')}
          </Button>
          <Button type="button" variant="ghost" disabled={!isSet || del.isPending} onClick={() => del.mutate({ orgId, conId: connection.id })}>
            {t('writeCredential.remove')}
          </Button>
        </div>
      </form>
    </section>
  )
}
