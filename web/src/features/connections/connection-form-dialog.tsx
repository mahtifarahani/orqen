import { useState, type FormEvent, type RefObject, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import {
  useCreateConnection,
  usePatchConnection,
  type Connection,
} from '@/api/hooks/connections'
import type { AuthMethod, Deployment, Provider } from '@/api/generated/model'
import { newIdempotencyKey } from '@/api/idempotency'
import { ApiProblem, ok } from '@/api/problem'
import { ConflictAlert, type ConflictRow } from '@/components/conflict-alert'
import { Field } from '@/components/field'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import { problemMessage } from '@/lib/problem-message'
import { ConnectionSetupGuide } from './connection-setup-guide'

const PROVIDERS: Provider[] = ['jira', 'gitlab', 'confluence', 'figma']
const DEPLOYMENTS: Deployment[] = ['cloud', 'server', 'data_center', 'self_managed']
const AUTH_METHODS: AuthMethod[] = ['api_token', 'personal_access_token', 'basic', 'oauth']

interface Draft {
  name: string
  provider: Provider
  deployment: Deployment
  base_url: string
  auth_method: AuthMethod
  auth_username: string
  timeout_ms: string
  max_concurrency: string
  allowed_hosts: string
  proxy_url: string
  custom_ca_pem: string
}

const EMPTY: Draft = {
  name: '',
  provider: 'jira',
  deployment: 'self_managed',
  base_url: '',
  auth_method: 'personal_access_token',
  auth_username: '',
  timeout_ms: '15000',
  max_concurrency: '4',
  allowed_hosts: '',
  proxy_url: '',
  custom_ca_pem: '',
}

const toDraft = (c: Connection): Draft => ({
  name: c.name,
  provider: c.provider,
  deployment: c.deployment,
  base_url: c.base_url,
  auth_method: c.auth_method,
  auth_username: c.auth_username ?? '',
  timeout_ms: String(c.timeout_ms),
  max_concurrency: String(c.max_concurrency),
  allowed_hosts: c.allowed_hosts.join(', '),
  proxy_url: c.proxy_url ?? '',
  custom_ca_pem: c.custom_ca_pem ?? '',
})

const hosts = (s: string) =>
  s
    .split(',')
    .map((h) => h.trim())
    .filter(Boolean)

const numOrNull = (s: string) => (s.trim() === '' ? null : Number(s))

function ConnectionForm({ orgId, connection, onDone }: { orgId: string; connection?: Connection; onDone: () => void }) {
  const { t } = useTranslation()
  const editing = !!connection
  // Create: one Idempotency-Key per dialog open. Edit: If-Match from the revision the draft is based on.
  const [key] = useState(newIdempotencyKey)
  const [base, setBase] = useState<Connection | undefined>(connection)
  const [draft, setDraft] = useState<Draft>(connection ? toDraft(connection) : EMPTY)
  const [conflict, setConflict] = useState<Connection | null>(null)
  const create = useCreateConnection(orgId, key)
  const patch = usePatchConnection(orgId, base?.revision ?? 0)
  const m = editing ? patch : create
  const set = <K extends keyof Draft>(k: K, v: Draft[K]) => setDraft((d) => ({ ...d, [k]: v }))

  const needsUsername = draft.provider === 'jira' && (draft.auth_method === 'basic' || (draft.deployment === 'cloud' && draft.auth_method === 'api_token'))

  const err = m.error
  const fieldErrors = err instanceof ApiProblem && err.code !== 'revision_conflict' ? err.fieldErrors() : {}
  const generalError = m.isError && !conflict && Object.keys(fieldErrors).length === 0 ? problemMessage(t, err) : undefined

  const onConflict = (e: unknown) => {
    if (e instanceof ApiProblem && e.code === 'revision_conflict' && e.current) setConflict(e.current as unknown as Connection)
  }

  const submit = (e: FormEvent) => {
    e.preventDefault()
    const common = {
      name: draft.name.trim(),
      deployment: draft.deployment,
      base_url: draft.base_url.trim(),
      auth_method: draft.auth_method,
      auth_username: draft.auth_username.trim(),
      timeout_ms: numOrNull(draft.timeout_ms),
      max_concurrency: numOrNull(draft.max_concurrency),
      proxy_url: draft.proxy_url.trim(),
      custom_ca_pem: draft.custom_ca_pem.trim(),
      allowed_hosts: hosts(draft.allowed_hosts).length ? hosts(draft.allowed_hosts) : null,
    }
    if (editing && base) {
      patch.mutate(
        { orgId, conId: base.id, data: common },
        {
          onSuccess: () => {
            toast.success(t('connections.form.saved'))
            onDone()
          },
          onError: onConflict,
        },
      )
    } else {
      create.mutate(
        {
          orgId,
          data: {
            ...common,
            provider: draft.provider,
            proxy_url: common.proxy_url || null,
            custom_ca_pem: common.custom_ca_pem || null,
          },
        },
        {
          onSuccess: (res) => {
            ok(res)
            toast.success(t('connections.form.created'))
            onDone()
          },
        },
      )
    }
  }

  const conflictRows: ConflictRow[] = conflict
    ? (
        [
          ['name', draft.name, conflict.name],
          ['deployment', draft.deployment, conflict.deployment],
          ['base_url', draft.base_url, conflict.base_url],
          ['auth_method', draft.auth_method, conflict.auth_method],
          ['auth_username', draft.auth_username, conflict.auth_username ?? ''],
          ['timeout_ms', draft.timeout_ms, String(conflict.timeout_ms)],
          ['max_concurrency', draft.max_concurrency, String(conflict.max_concurrency)],
          ['allowed_hosts', draft.allowed_hosts, conflict.allowed_hosts.join(', ')],
          ['proxy_url', draft.proxy_url, conflict.proxy_url ?? ''],
          ['custom_ca_pem', draft.custom_ca_pem, conflict.custom_ca_pem ?? ''],
        ] as const
      )
        .filter(([, mine, latest]) => mine !== latest)
        .map(([k, mine, latest]) => ({ label: t(`connections.conflict.fields.${k}`), mine, latest }))
    : []

  const selectField = <T extends string>(
    id: string,
    label: string,
    value: T,
    options: readonly T[],
    labelOf: (o: T) => string,
    onChange: (v: T) => void,
    disabled = false,
    hint?: ReactNode,
  ) => (
    <Field collapsibleHint id={id} label={label} hint={hint}>
      {(p) => (
        <Select value={value} onValueChange={(v) => onChange(v as T)} disabled={disabled}>
          <SelectTrigger {...p} className="w-full min-w-0 [&_[data-slot=select-value]]:min-w-0">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {options.map((o) => (
              <SelectItem key={o} value={o}>
                {labelOf(o)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      )}
    </Field>
  )

  return (
    <form onSubmit={submit} className="flex min-h-0 min-w-0 flex-col gap-4">
      <div className="min-h-0 min-w-0 space-y-4 overflow-y-auto p-1 [overflow-wrap:anywhere] [&>div]:min-w-0">
        {conflict && (
          <ConflictAlert
            rows={conflictRows}
            onLoadLatest={() => {
              setBase(conflict)
              setDraft(toDraft(conflict))
              setConflict(null)
              patch.reset()
            }}
            onKeepMine={() => {
              setBase(conflict)
              setConflict(null)
              patch.reset()
            }}
          />
        )}
        <Field collapsibleHint id="con-name" label={t('connections.form.name')} hint={t('connections.form.nameHint')} error={fieldErrors.name}>
          {(p) => <Input {...p} required value={draft.name} onChange={(e) => set('name', e.target.value)} autoComplete="off" />}
        </Field>
        {selectField('con-provider', t('connections.form.provider'), draft.provider, PROVIDERS, (o) => t(`providers.${o}`), (v) => set('provider', v), editing, editing ? t('connections.form.providerLocked') : t('connections.form.providerHint'))}
        {selectField('con-deployment', t('connections.form.deployment'), draft.deployment, DEPLOYMENTS, (o) => t(`deployments.${o}`), (v) => set('deployment', v), false, t(`connections.form.guide.${draft.deployment}`))}
        <Field collapsibleHint id="con-base-url" label={t('connections.form.baseUrl')} hint={t('connections.form.baseUrlHint')} error={fieldErrors.base_url}>
          {(p) => (
            <Input {...p} dir="ltr" required placeholder="https://jira.example.com/jira" value={draft.base_url} onChange={(e) => set('base_url', e.target.value)} autoComplete="off" />
          )}
        </Field>
        {selectField('con-auth', t('connections.form.authMethod'), draft.auth_method, AUTH_METHODS, (o) => t(`authMethods.${o}`), (v) => set('auth_method', v), false, <ConnectionSetupGuide provider={draft.provider} deployment={draft.deployment} authMethod={draft.auth_method} />)}
        {needsUsername && (
          <Field collapsibleHint id="con-username" label={t('connections.form.username')} hint={t('connections.form.usernameHint')} error={fieldErrors.auth_username}>
            {(p) => <Input {...p} dir="ltr" required maxLength={254} value={draft.auth_username} onChange={(e) => set('auth_username', e.target.value)} autoComplete="off" />}
          </Field>
        )}
        <div className="grid min-w-0 grid-cols-1 gap-4 sm:grid-cols-2 [&>div]:min-w-0">
          <Field collapsibleHint id="con-timeout" label={t('connections.form.timeoutMs')} hint={t('connections.form.timeoutHint')} error={fieldErrors.timeout_ms}>
            {(p) => <Input {...p} dir="ltr" type="number" min={1000} max={120000} value={draft.timeout_ms} onChange={(e) => set('timeout_ms', e.target.value)} />}
          </Field>
          <Field collapsibleHint id="con-concurrency" label={t('connections.form.maxConcurrency')} hint={t('connections.form.concurrencyHint')} error={fieldErrors.max_concurrency}>
            {(p) => <Input {...p} dir="ltr" type="number" min={1} max={32} value={draft.max_concurrency} onChange={(e) => set('max_concurrency', e.target.value)} />}
          </Field>
        </div>
        <Field collapsibleHint id="con-hosts" label={t('connections.form.allowedHosts')} hint={t('connections.form.hostsHint')} error={fieldErrors.allowed_hosts}>
          {(p) => <Input {...p} dir="ltr" value={draft.allowed_hosts} onChange={(e) => set('allowed_hosts', e.target.value)} autoComplete="off" />}
        </Field>
        <Field collapsibleHint id="con-proxy" label={t('connections.form.proxyUrl')} hint={t('connections.form.proxyHint')} error={fieldErrors.proxy_url}>
          {(p) => <Input {...p} dir="ltr" value={draft.proxy_url} onChange={(e) => set('proxy_url', e.target.value)} autoComplete="off" />}
        </Field>
        <Field collapsibleHint id="con-ca" label={t('connections.form.customCa')} hint={t('connections.form.caHint')} error={fieldErrors.custom_ca_pem}>
          {(p) => <Textarea {...p} dir="ltr" rows={4} className="min-w-0 font-mono text-xs" value={draft.custom_ca_pem} onChange={(e) => set('custom_ca_pem', e.target.value)} />}
        </Field>
        {generalError && (
          <Alert variant="destructive" role="alert">
            <AlertDescription>{generalError}</AlertDescription>
          </Alert>
        )}
      </div>
      <DialogFooter className="m-0 shrink-0 rounded-lg">
        <Button type="button" variant="outline" onClick={onDone}>
          {t('common.cancel')}
        </Button>
        <Button type="submit" disabled={m.isPending}>
          {editing ? t('common.save') : t('connections.form.submitCreate')}
        </Button>
      </DialogFooter>
    </form>
  )
}

export function ConnectionFormDialog({
  orgId,
  connection,
  open,
  onOpenChange,
  returnFocusRef,
}: {
  orgId: string
  /** Present when editing. */
  connection?: Connection
  open: boolean
  onOpenChange: (open: boolean) => void
  returnFocusRef?: RefObject<HTMLElement | null>
}) {
  const { t } = useTranslation()
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        className="flex max-h-[calc(100dvh-2rem)] min-w-0 flex-col sm:max-w-xl"
        onCloseAutoFocus={(e) => {
          if (returnFocusRef?.current) {
            e.preventDefault()
            returnFocusRef.current.focus()
          }
        }}
      >
        <DialogHeader>
          <DialogTitle>{connection ? t('connections.form.editTitle') : t('connections.form.createTitle')}</DialogTitle>
          <DialogDescription>{t('connections.form.description')}</DialogDescription>
        </DialogHeader>
        {/* mounted only while open: fresh draft and a fresh idempotency key each time */}
        <ConnectionForm orgId={orgId} connection={connection} onDone={() => onOpenChange(false)} />
      </DialogContent>
    </Dialog>
  )
}
