import { useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router'
import { useSetup, useSetupActions } from '@/api/hooks/setup'
import { ConnectionsPage } from '@/features/connections/connections-page'
import { OrgSettingsPage } from '@/features/orgs/org-settings-page'
import { OrgPolicyPanel } from '@/features/policy/policy-panel'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { Button } from '@/components/ui/button'
import type { SetupWizardDto } from '@/api/generated/model'
import { HostCard, KeysCard } from '@/features/models/orchestrator-keys-tab'
import { ProfileQuickStart } from './profile-quick-start'
import { ProjectsStep } from './projects-step'
import { ReviewPanel } from './review-panel'
import { StepFooter } from './step-footer'
import { Stepper } from './stepper'
import { SystemCheckPanel } from './system-check-panel'

const KEYS = ['organization', 'connections', 'projects', 'profile', 'policy', 'system', 'review']

/** Which step is open: `?step=` wins, then the step the server remembers, then the first. */
function resolveStep(urlStep: string | null, wizard: Pick<SetupWizardDto, 'current_step'>): string {
  if (urlStep && KEYS.includes(urlStep)) return urlStep
  return KEYS.includes(wizard.current_step) ? wizard.current_step : KEYS[0]
}

export function SetupPage() {
  const { t } = useTranslation()
  const { orgId = '' } = useParams()
  const setup = useSetup(orgId)
  return (
    <div className="flex flex-col gap-6">
      <div>
        <h1 className="text-2xl font-semibold">{t('wizard.title')}</h1>
        <p className="max-w-prose text-sm text-muted-foreground">{t('wizard.subtitle')}</p>
      </div>
      {setup.isPending && <LoadingBlock />}
      {setup.isError && <LoadFailed title={t('wizard.loadFailed')} error={setup.error} onRetry={() => void setup.refetch()} />}
      {setup.data && <Wizard orgId={orgId} wizard={setup.data} />}
    </div>
  )
}

function Wizard({ orgId, wizard }: { orgId: string; wizard: SetupWizardDto }) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const [search, setSearch] = useSearchParams()
  const actions = useSetupActions(orgId, wizard.revision)
  const step = resolveStep(search.get('step'), wizard)
  const index = KEYS.indexOf(step)
  const current = wizard.steps.find((s) => s.key === step) ?? wizard.steps[0]
  const busy = Object.values(actions).some((m) => m.isPending)
  const panelRef = useRef<HTMLElement>(null)

  const go = (key: string) => setSearch({ step: key }, { replace: false })
  // Resume position: the server remembers the open step. Synced after the revision from the last write has arrived.
  const synced = useRef(wizard.current_step)
  useEffect(() => {
    if (busy || step === wizard.current_step || synced.current === step) return
    synced.current = step
    actions.patch.mutate({ orgId, data: { current_step: step } })
  })
  useEffect(() => {
    synced.current = wizard.current_step
  }, [wizard.current_step])
  // Moving to another step puts keyboard focus on its heading.
  useEffect(() => {
    panelRef.current?.focus()
  }, [step])

  const footer = {
    complete: (key: string) => actions.complete.mutateAsync({ orgId, stepKey: key }),
    skip: (key: string, reason: string | null) => actions.skip.mutateAsync({ orgId, stepKey: key, data: { reason } }),
    reopen: (key: string) => actions.reopen.mutateAsync({ orgId, stepKey: key }),
    finish: () => actions.finish.mutateAsync({ orgId }).then(() => navigate(`/orgs/${orgId}/overview`)),
  }
  const next = () => go(KEYS[Math.min(index + 1, KEYS.length - 1)])
  const exit = (dismiss: boolean) =>
    actions.patch.mutate({ orgId, data: dismiss ? { current_step: step, dismissed: true } : { current_step: step } }, { onSettled: () => navigate(`/orgs/${orgId}/overview`) })

  return (
    <div className="grid gap-6 lg:grid-cols-[16rem_1fr]">
      <nav aria-label={t('wizard.stepsLabel')} className="flex flex-col gap-4">
        <Stepper steps={wizard.steps} current={step} onSelect={go} />
        <div className="flex flex-col gap-2">
          <Button variant="outline" size="sm" onClick={() => exit(false)} disabled={busy}>
            {t('wizard.actions.saveExit')}
          </Button>
          <Button variant="ghost" size="sm" onClick={() => exit(true)} disabled={busy}>
            {t('wizard.actions.hide')}
          </Button>
        </div>
      </nav>
      <section ref={panelRef} tabIndex={-1} aria-labelledby="wizard-step-title" className="flex min-w-0 flex-col gap-4 outline-none">
        <div>
          <h2 id="wizard-step-title" className="text-xl font-semibold">
            {t(`wizard.steps.${step}.title`)}
          </h2>
          <p className="max-w-prose text-sm text-muted-foreground">{t(`wizard.steps.${step}.description`)}</p>
        </div>
        {current.status === 'skipped' && <p className="text-sm text-muted-foreground" role="status">{t('wizard.skippedNote')}</p>}
        {current.attention && <p className="text-sm text-destructive" role="status">{t('wizard.attentionNote')}</p>}
        {current.missing.length > 0 && current.status !== 'done' && (
          <div className="rounded-lg border p-3 text-sm">
            <p className="mb-1 font-medium">{t('wizard.stillMissing')}</p>
            <ul className="list-disc ps-5 text-muted-foreground">
              {current.missing.map((m) => (
                <li key={m.code}>{t(`wizard.missing.${m.code}`, { defaultValue: m.detail })}</li>
              ))}
            </ul>
          </div>
        )}
        <div className="min-w-0">
          {step === 'organization' && <OrgSettingsPage embedded />}
          {step === 'connections' && (
            <div className="flex flex-col gap-4">
              <ConnectionsPage embedded />
              <p className="text-xs text-muted-foreground">{t('wizard.otherProviders')}</p>
            </div>
          )}
          {step === 'projects' && <ProjectsStep orgId={orgId} />}
          {step === 'profile' && (
            <div className="flex flex-col gap-6">
              <section aria-label={t('wizard.hostAndKeys')} className="flex flex-col gap-4">
                <HostCard orgId={orgId} />
                <KeysCard orgId={orgId} />
              </section>
              <ProfileQuickStart orgId={orgId} />
            </div>
          )}
          {step === 'policy' && <OrgPolicyPanel orgId={orgId} />}
          {step === 'system' && <SystemCheckPanel orgId={orgId} embedded />}
          {step === 'review' && <ReviewPanel orgId={orgId} wizard={wizard} goStep={go} />}
        </div>
        <StepFooter
          step={current}
          first={index === 0}
          last={index === KEYS.length - 1}
          busy={busy}
          actions={footer}
          onBack={() => go(KEYS[Math.max(index - 1, 0)])}
          onForward={next}
          onMissing={() => panelRef.current?.scrollIntoView?.({ block: 'start' })}
        />
        <p className="text-xs text-muted-foreground">
          <Link to={`/orgs/${orgId}/overview`} className="underline underline-offset-4">
            {t('wizard.toOverview')}
          </Link>
        </p>
      </section>
    </div>
  )
}
