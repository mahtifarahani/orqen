import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router'
import { useCompleteSetupStep } from '@/api/generated/setup/setup'
import { CreateOrgForm } from '@/features/orgs/create-org-dialog'
import { setLastOrg } from '@/lib/last-org'

/** First step before any organization exists: the same fields as "Create organization", inline. */
export function NewOrgStep() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  // A brand-new wizard has revision 0; creating the organization is this step's confirmation.
  const complete = useCompleteSetupStep({ request: { headers: { 'If-Match': '"0"' } } })
  return (
    <main id="main" tabIndex={-1} className="mx-auto flex min-h-svh max-w-xl flex-col gap-6 p-6 outline-none">
      <div>
        <p className="text-sm text-muted-foreground">{t('wizard.title')}</p>
        <h1 className="text-2xl font-semibold">{t('wizard.steps.organization.title')}</h1>
        <p className="mt-1 text-sm text-muted-foreground">{t('wizard.newOrgHelp')}</p>
      </div>
      <CreateOrgForm
        submitLabel={t('wizard.actions.next')}
        onCreated={(org) => {
          setLastOrg(org.id)
          const next = () => navigate(`/orgs/${org.id}/setup?step=connections`, { replace: true })
          complete.mutate({ orgId: org.id, stepKey: 'organization' }, { onSettled: next })
        }}
      />
    </main>
  )
}
