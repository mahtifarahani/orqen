import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import {
  useBindProject,
  useDetectRun,
  useExtension,
  useExtensionActions,
  useExtensionCandidates,
  useHealthRun,
  useTestRun,
  type ExtensionDetail,
} from '@/api/hooks/extensions'
import { projectRows, useProjects } from '@/api/hooks/projects'
import { Field } from '@/components/field'
import { Ltr } from '@/components/ltr'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { useOperationRunner } from '@/components/use-operation-runner'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { Switch } from '@/components/ui/switch'
import { formatDateTime } from '@/lib/format'
import { problemMessage } from '@/lib/problem-message'
import { useLocale } from '@/lib/use-locale'
import { ConfigForm } from './config-form'
import { InstallGuidance } from './install-guidance'
import { LicenseDialog } from './license-dialog'
import { stateVariant } from './state-variant'

/** The detail view of one extension: binary, license, detection, test, enablement, config, credentials, installs. */
export function ExtensionSheet({ orgId, extId, onClose }: { orgId: string; extId: string | null; onClose: () => void }) {
  const { t } = useTranslation()
  const q = useExtension(orgId, extId)
  const name = extId ? t(`extensions.names.${extId}`, { defaultValue: extId }) : ''
  return (
    <Sheet open={!!extId} onOpenChange={(o) => !o && onClose()}>
      <SheetContent side="end" className="w-full overflow-y-auto sm:max-w-xl" data-testid="extension-sheet">
        <SheetHeader>
          <SheetTitle>{name}</SheetTitle>
          <SheetDescription>{t('extensions.sheetHelp')}</SheetDescription>
        </SheetHeader>
        <div className="flex flex-col gap-6 px-4 pb-6">
          {q.isPending && !!extId && <LoadingBlock />}
          {q.isError && <LoadFailed title={name} error={q.error} onRetry={() => q.refetch()} />}
          {q.data && <ExtensionBody orgId={orgId} detail={q.data} />}
        </div>
      </SheetContent>
    </Sheet>
  )
}

function ExtensionBody({ orgId, detail }: { orgId: string; detail: ExtensionDetail }) {
  const { t } = useTranslation()
  const locale = useLocale()
  const id = detail.manifest.id
  const actions = useExtensionActions(orgId, detail.revision)
  const detectRunner = useOperationRunner(orgId)
  const testRunner = useOperationRunner(orgId)
  const healthRunner = useOperationRunner(orgId)
  const detect = useDetectRun(orgId, detectRunner.key)
  const test = useTestRun(orgId, testRunner.key)
  const health = useHealthRun(orgId, healthRunner.key)
  const [path, setPath] = useState('')
  const [findOpen, setFindOpen] = useState(false)
  const [licenseOpen, setLicenseOpen] = useState(false)
  const candidates = useExtensionCandidates(orgId, id, findOpen)
  const fail = (e: unknown) => toast.error(problemMessage(t, e))

  const state = detail.state
  const gated = detail.manifest.license.gate === 'acceptance_required'
  const accepted = !!detail.license_state && !detail.license_state.revoked_at
  const needsLicense = gated && !accepted
  const registered = !!detail.path
  const detectOnly = detail.manifest.integration === 'detect_only'
  const enabled = state === 'enabled'
  // A tested or disabled extension is not usable yet only because it is not enabled; any other reason (revoked licence, changed binary...) blocks.
  const canEnable = !detectOnly && !enabled && (state === 'tested' || state === 'disabled') && (detail.usable.ok !== false || detail.usable.reason === 'disabled')
  const detectBlocked = needsLicense ? t('extensions.detectNeedsLicense') : !registered ? t('extensions.detectNeedsBinary') : null

  return (
    <>
      <section className="flex flex-col gap-2" aria-label={t('extensions.status')}>
        <div className="flex flex-wrap items-center gap-2">
          <Badge variant={stateVariant(state)} data-ext-state={state}>
            {t(`extensions.state.${state}`, { defaultValue: state })}
          </Badge>
          <Badge variant="outline">{t(`extensions.kind.${detail.manifest.kind}`)}</Badge>
          <Badge variant="outline">{t(`extensions.integration.${detail.manifest.integration}`)}</Badge>
          <Ltr className="text-xs text-muted-foreground">v{detail.manifest.version}</Ltr>
        </div>
        {!detail.usable.ok && detail.usable.reason && (
          <p className="text-sm text-muted-foreground" role="status">
            {t(`extensions.unusable.${detail.usable.reason}`, { defaultValue: detail.usable.reason })}
          </p>
        )}
        {detail.health && (
          <p className="text-sm" role="status">
            {t(`extensions.healthStatus.${detail.health.status}`)} · {formatDateTime(detail.health.at, locale)}
          </p>
        )}
        {detail.last_error && <p className="text-sm text-destructive">{detail.last_error}</p>}
        {detectOnly && <p className="text-sm text-muted-foreground">{t('extensions.detectOnlyNote')}</p>}
      </section>

      <InstallGuidance detail={detail} />

      <section className="flex flex-col gap-3" aria-label={t('extensions.binary.title')}>
        <h3 className="font-medium">{t('extensions.binary.title')}</h3>
        {registered && (
          <p className="text-sm">
            <Ltr className="break-all">{detail.path}</Ltr>
          </p>
        )}
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            if (!path.trim()) return
            actions.register.mutate({ orgId, extId: id, data: { path: path.trim() } }, { onSuccess: () => setPath(''), onError: fail })
          }}
        >
          <Field id="ext-path" label={t('extensions.binary.path')} hint={t('extensions.binary.hint')}>
            {(f) => <Input {...f} dir="ltr" autoComplete="off" value={path} onChange={(e) => setPath(e.target.value)} />}
          </Field>
          <div className="flex flex-wrap gap-2">
            <Button type="submit" disabled={!path.trim() || actions.register.isPending}>
              {t('extensions.binary.register')}
            </Button>
            <Button type="button" variant="outline" onClick={() => setFindOpen(true)} disabled={candidates.isFetching}>
              {t('extensions.binary.find')}
            </Button>
          </div>
        </form>
        {findOpen && candidates.data && (
          <ul className="flex flex-col gap-1" aria-label={t('extensions.binary.candidates')}>
            {candidates.data.paths.length === 0 && <li className="text-sm text-muted-foreground">{t('extensions.binary.noCandidates')}</li>}
            {candidates.data.paths.map((p) => (
              <li key={p}>
                <Button type="button" variant="ghost" size="sm" className="h-auto w-full justify-start whitespace-normal text-start" onClick={() => setPath(p)}>
                  <Ltr className="break-all">{p}</Ltr>
                </Button>
              </li>
            ))}
          </ul>
        )}
      </section>

      {gated && (
        <section className="flex flex-col gap-2" aria-label={t('extensions.license.section')}>
          <h3 className="font-medium">{t('extensions.license.section')}</h3>
          <p className="text-sm text-muted-foreground">
            {accepted && detail.license_state
              ? t('extensions.license.acceptedOn', { date: formatDateTime(detail.license_state.accepted_at, locale), basis: t(`extensions.license.bases.${detail.license_state.basis}`, { defaultValue: detail.license_state.basis }) })
              : t('extensions.license.required')}
          </p>
          <div>
            {accepted ? (
              <Button variant="outline" disabled={actions.revokeLicense.isPending} onClick={() => actions.revokeLicense.mutate({ orgId, extId: id }, { onError: fail })}>
                {t('extensions.license.revoke')}
              </Button>
            ) : (
              <Button onClick={() => setLicenseOpen(true)}>{t('extensions.license.review')}</Button>
            )}
          </div>
          <LicenseDialog
            detail={detail}
            open={licenseOpen}
            onOpenChange={setLicenseOpen}
            pending={actions.acceptLicense.isPending}
            error={actions.acceptLicense.error}
            onAccept={(data) => actions.acceptLicense.mutate({ orgId, extId: id, data }, { onSuccess: () => setLicenseOpen(false) })}
          />
        </section>
      )}

      <section className="flex flex-col gap-3" aria-label={t('extensions.actions.title')}>
        <h3 className="font-medium">{t('extensions.actions.title')}</h3>
        {detectBlocked && (
          <p className="text-sm text-muted-foreground" data-testid="detect-blocked">
            {detectBlocked}
          </p>
        )}
        <div className="flex flex-wrap gap-2">
          <Button
            variant="outline"
            disabled={!!detectBlocked || detect.isPending || detectRunner.running}
            onClick={() => detect.mutate({ orgId, extId: id }, { onSuccess: detectRunner.started, onError: fail })}
          >
            {t(detectRunner.running ? 'extensions.actions.detecting' : 'extensions.actions.detect')}
          </Button>
          <Button
            variant="outline"
            disabled={!detail.detected || test.isPending || testRunner.running}
            onClick={() => test.mutate({ orgId, extId: id }, { onSuccess: testRunner.started, onError: fail })}
          >
            {t(testRunner.running ? 'extensions.actions.testing' : 'extensions.actions.test')}
          </Button>
          <Button
            variant="outline"
            disabled={!registered || health.isPending || healthRunner.running}
            onClick={() => health.mutate({ orgId, extId: id }, { onSuccess: healthRunner.started, onError: fail })}
          >
            {t('extensions.actions.health')}
          </Button>
          {!detectOnly && !enabled && (
            <Button disabled={!canEnable || actions.enable.isPending} onClick={() => actions.enable.mutate({ orgId, extId: id }, { onError: fail })}>
              {t('extensions.actions.enable')}
            </Button>
          )}
          {enabled && (
            <Button variant="outline" disabled={actions.disable.isPending} onClick={() => actions.disable.mutate({ orgId, extId: id }, { onError: fail })}>
              {t('extensions.actions.disable')}
            </Button>
          )}
        </div>
        {detail.detected && (
          <p className="text-sm text-muted-foreground">
            {t('extensions.detected')} <Ltr>{detail.detected.version}</Ltr> · <Ltr className="break-all">{detail.detected.sha256.slice(0, 12)}</Ltr>
          </p>
        )}
        {detail.last_test && (
          <p className="text-sm" role="status">
            {t(detail.last_test.ok ? 'extensions.testOk' : 'extensions.testFailed')}
            {detail.last_test.code && <span className="text-muted-foreground"> · {t(`problems.${detail.last_test.code}`, { defaultValue: detail.last_test.code })}</span>} · {formatDateTime(detail.last_test.at, locale)}
          </p>
        )}
        {(detect.isError || test.isError || health.isError || actions.enable.isError || actions.disable.isError || actions.register.isError || actions.revokeLicense.isError) && (
          <Alert variant="destructive" role="alert">
            <AlertDescription>
              {problemMessage(t, detect.error ?? test.error ?? health.error ?? actions.enable.error ?? actions.disable.error ?? actions.register.error ?? actions.revokeLicense.error)}
            </AlertDescription>
          </Alert>
        )}
      </section>

      <ConfigForm key={detail.revision} detail={detail} pending={actions.setConfig.isPending} onSave={(config) => actions.setConfig.mutate({ orgId, extId: id, data: { config } }, { onSuccess: () => toast.success(t('extensions.config.saved')), onError: fail })} />

      {detail.manifest.credential_fields.length > 0 && (
        <section className="flex flex-col gap-3" aria-label={t('extensions.credentials.title')}>
          <h3 className="font-medium">{t('extensions.credentials.title')}</h3>
          <p className="text-sm text-muted-foreground">{t('extensions.credentials.help')}</p>
          {detail.manifest.credential_fields.map((cf) => (
            <CredentialRow
              key={cf.field}
              label={t(cf.label_key, { defaultValue: cf.field })}
              field={cf.field}
              isSet={detail.credentials.some((c) => c.field === cf.field && c.set)}
              pending={actions.setCredential.isPending || actions.deleteCredential.isPending}
              onSave={(value, done) => actions.setCredential.mutate({ orgId, extId: id, field: cf.field, data: { value } }, { onSettled: done, onError: fail })}
              onRemove={() => actions.deleteCredential.mutate({ orgId, extId: id, field: cf.field }, { onError: fail })}
            />
          ))}
        </section>
      )}

      {detail.installs.length > 0 && (
        <section className="flex flex-col gap-2" aria-label={t('extensions.installs.title')}>
          <h3 className="font-medium">{t('extensions.installs.title')}</h3>
          <ul className="flex flex-col gap-2">
            {detail.installs.map((i) => (
              <li key={i.id} className="flex flex-wrap items-center justify-between gap-2 rounded-md border p-2 text-sm">
                <span>
                  #{i.number} · <Ltr>{i.version}</Ltr> · {formatDateTime(i.tested_at, locale)}
                </span>
                {i.active ? (
                  <Badge>{t('extensions.installs.active')}</Badge>
                ) : (
                  <Button size="sm" variant="outline" disabled={actions.rollback.isPending} onClick={() => actions.rollback.mutate({ orgId, extId: id, data: { install_id: i.id } }, { onError: fail })}>
                    {t('extensions.installs.rollback')}
                  </Button>
                )}
              </li>
            ))}
          </ul>
        </section>
      )}

      <ProjectBindings orgId={orgId} detail={detail} disabled={!enabled} />
    </>
  )
}

/** Write-only secret field: never prefilled, emptied as soon as the request settles. */
function CredentialRow({ label, field, isSet, pending, onSave, onRemove }: { label: string; field: string; isSet: boolean; pending: boolean; onSave: (value: string, done: () => void) => void; onRemove: () => void }) {
  const { t } = useTranslation()
  const [value, setValue] = useState('')
  return (
    <form
      className="flex flex-col gap-2"
      onSubmit={(e) => {
        e.preventDefault()
        if (value) onSave(value, () => setValue(''))
      }}
    >
      <Field id={`ext-cred-${field}`} label={label} hint={isSet ? t('extensions.credentials.isSet') : t('extensions.credentials.notSet')}>
        {(f) => <Input {...f} dir="ltr" type="password" autoComplete="off" placeholder={isSet ? '••••••••' : ''} value={value} onChange={(e) => setValue(e.target.value)} />}
      </Field>
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant="outline" data-credential-set={isSet}>
          {t(isSet ? 'extensions.credentials.set' : 'extensions.credentials.none')}
        </Badge>
        <Button type="submit" size="sm" disabled={!value || pending}>
          {t(isSet ? 'extensions.credentials.replace' : 'extensions.credentials.save')}
        </Button>
        <Button type="button" size="sm" variant="ghost" disabled={!isSet || pending} onClick={onRemove}>
          {t('common.remove')}
        </Button>
      </div>
    </form>
  )
}

function ProjectBindings({ orgId, detail, disabled }: { orgId: string; detail: ExtensionDetail; disabled: boolean }) {
  const { t } = useTranslation()
  const projects = useProjects(orgId, '')
  const rows = projectRows(projects.data)
  if (rows.length === 0) return null
  return (
    <section className="flex flex-col gap-2" aria-label={t('extensions.projects.title')}>
      <h3 className="font-medium">{t('extensions.projects.title')}</h3>
      {disabled && <p className="text-sm text-muted-foreground">{t('extensions.projects.needsEnabled')}</p>}
      <ul className="flex flex-col gap-2">
        {rows.map((p) => (
          <ProjectBindingRow key={p.id} orgId={orgId} extId={detail.manifest.id} project={p} binding={detail.project_bindings.find((b) => b.project_id === p.id)} locked={disabled} />
        ))}
      </ul>
    </section>
  )
}

function ProjectBindingRow({ orgId, extId, project, binding, locked }: { orgId: string; extId: string; project: { id: string; name: string }; binding: { enabled: boolean; revision: number } | undefined; locked: boolean }) {
  const { t } = useTranslation()
  const bind = useBindProject(orgId, binding?.revision ?? 0)
  const on = !!binding?.enabled
  return (
    <li className="flex items-center justify-between gap-2 text-sm">
      <span className="min-w-0 truncate">{project.name}</span>
      <Switch
        checked={on}
        disabled={(locked && !on) || bind.isPending}
        aria-label={t('extensions.projects.toggle', { name: project.name })}
        onCheckedChange={(enabled) => bind.mutate({ orgId, prjId: project.id, extId, data: { enabled } }, { onError: (e) => toast.error(problemMessage(t, e)) })}
      />
    </li>
  )
}
