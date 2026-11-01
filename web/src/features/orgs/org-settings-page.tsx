import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Link, useParams, useSearchParams } from 'react-router'
import { toast } from 'sonner'
import { useOrg, usePatchOrg, type Org } from '@/api/hooks/orgs'
import { ApiProblem, ok } from '@/api/problem'
import { ConflictAlert } from '@/components/conflict-alert'
import { Field } from '@/components/field'
import { Ltr } from '@/components/ltr'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { OrgPolicyPanel } from '@/features/policy/policy-panel'
import { PublishingPanel } from '@/features/publishing/publishing-panel'
import { RulesPanel } from '@/features/rules/rules-panel'
import { BackupsSettingsTab } from './backups-settings-tab'
import { OrgMaintenanceTab } from './maintenance-tab'
import { AutomationCard } from '@/features/overview/automation-card'
import { isLocale, type Locale } from '@/i18n/direction'
import { problemMessage } from '@/lib/problem-message'

interface Draft {
  name: string
  locale: Locale
  timezone: string
}
const toDraft = (o: Org): Draft => ({ name: o.name, locale: o.locale, timezone: o.timezone })
const sameDraft = (a: Draft, b: Draft) => a.name === b.name && a.locale === b.locale && a.timezone === b.timezone

export function OrgSettingsPage({ embedded = false }: { embedded?: boolean } = {}) {
  const { t } = useTranslation()
  const { orgId = '' } = useParams()
  const org = useOrg(orgId)
  if (org.isPending) return <Skeleton className="h-64 max-w-xl" />
  if (org.isError) return <Alert variant="destructive"><AlertDescription>{problemMessage(t, org.error)}</AlertDescription></Alert>
  if (embedded) return <OrgSettingsForm key={orgId} org={org.data} embedded />
  return <SettingsTabs orgId={orgId} org={org.data} />
}

function SettingsTabs({ orgId, org }: { orgId: string; org: Org }) {
  const { t } = useTranslation()
  const [search, setSearch] = useSearchParams()
  const raw = search.get('tab')
  const tab = raw === 'policy' || raw === 'rules' || raw === 'publishing' || raw === 'backups' || raw === 'maintenance' ? raw : 'general'
  return (
    <div className="flex flex-col gap-4">
      <Tabs value={tab} onValueChange={(v) => setSearch({ tab: v }, { replace: true })}>
        <TabsList aria-label={t('orgs.settings.sections')}>
          <TabsTrigger value="general">{t('orgs.settings.tabs.general')}</TabsTrigger>
          <TabsTrigger value="policy">{t('orgs.settings.tabs.policy')}</TabsTrigger>
          <TabsTrigger value="rules">{t('orgs.settings.tabs.rules')}</TabsTrigger>
          <TabsTrigger value="publishing">{t('orgs.settings.tabs.publishing')}</TabsTrigger>
          <TabsTrigger value="backups">{t('orgs.settings.tabs.backups')}</TabsTrigger>
          <TabsTrigger value="maintenance">{t('orgs.settings.tabs.maintenance')}</TabsTrigger>
        </TabsList>
        <TabsContent value="general" className="pt-3">
          <OrgSettingsForm key={orgId} org={org} />
          <p className="mt-6 text-sm">
            <Link to={`/orgs/${orgId}/setup`} className="underline underline-offset-4">
              {t('orgs.settings.setupLink')}
            </Link>
          </p>
        </TabsContent>
        <TabsContent value="rules" className="pt-3">
          <div className="flex max-w-3xl flex-col gap-6">
            <AutomationCard orgId={orgId} />
            <RulesPanel orgId={orgId} />
          </div>
        </TabsContent>
        <TabsContent value="publishing" className="pt-3">
          <PublishingPanel orgId={orgId} />
        </TabsContent>
        <TabsContent value="backups" className="pt-3">
          <BackupsSettingsTab orgId={orgId} />
        </TabsContent>
        <TabsContent value="maintenance" className="pt-3">
          <OrgMaintenanceTab orgId={orgId} />
        </TabsContent>
        <TabsContent value="policy" className="pt-3">
          <div className="flex max-w-4xl flex-col gap-3">
            <div>
              <h1 className="text-2xl font-semibold">{t('policy.orgTitle')}</h1>
              <p className="max-w-prose text-sm text-muted-foreground">{t('policy.orgSubtitle')}</p>
            </div>
            <OrgPolicyPanel orgId={orgId} />
          </div>
        </TabsContent>
      </Tabs>
    </div>
  )
}

function OrgSettingsForm({ org, embedded = false }: { org: Org; embedded?: boolean }) {
  const { t } = useTranslation()
    // `base` is the server state the draft is built on; its revision goes into If-Match.
  const [base, setBase] = useState<Org>(org)
  const patch = usePatchOrg(org.id, base.revision)
  const [draft, setDraft] = useState<Draft>(toDraft(org))
  const [conflict, setConflict] = useState<Org | null>(null)
  const dirty = !sameDraft(draft, toDraft(base))

  // Follow server updates only while the user has not edited anything (derived during render).
  if (!dirty && org.revision > base.revision) {
    setBase(org)
    setDraft(toDraft(org))
  }

  const fieldErrors = patch.error instanceof ApiProblem && patch.error.code !== 'revision_conflict' ? patch.error.fieldErrors() : {}
  const generalError =
    patch.isError && !conflict && Object.keys(fieldErrors).length === 0 ? problemMessage(t, patch.error) : undefined

  const submit = (e: FormEvent) => {
    e.preventDefault()
    patch.mutate(
      { orgId: org.id, data: { name: draft.name.trim(), locale: draft.locale, timezone: draft.timezone.trim() } },
      {
        onSuccess: (res) => {
          const saved = ok(res)
          setBase(saved)
          setDraft(toDraft(saved))
          setConflict(null)
          toast.success(t('orgs.settings.saved'))
        },
        onError: (err) => {
          if (err instanceof ApiProblem && err.code === 'revision_conflict' && err.current) {
            setConflict(err.current as unknown as Org)
          }
        },
      },
    )
  }

  const rows = conflict
    ? (['name', 'locale', 'timezone'] as const)
        .filter((k) => draft[k] !== conflict[k])
        .map((k) => ({ label: t(`orgs.fields.${k}`), mine: String(draft[k]), latest: String(conflict[k]) }))
    : []

  return (
    <form onSubmit={submit} className="flex max-w-xl flex-col gap-4">
      <div>
        {!embedded && <h1 className="text-2xl font-semibold">{t('orgs.settings.title')}</h1>}
        <p className="text-sm text-muted-foreground">
          {t('orgs.settings.revision')}: <Ltr>{base.revision}</Ltr>
        </p>
      </div>
      {conflict && (
        <ConflictAlert
          rows={rows}
          onLoadLatest={() => {
            setBase(conflict)
            setDraft(toDraft(conflict))
            setConflict(null)
            patch.reset()
          }}
          onKeepMine={() => {
            setBase(conflict) // rebase onto the latest revision, keep what the user typed
            setConflict(null)
            patch.reset()
          }}
        />
      )}
      <Field id="set-slug" label={t('orgs.fields.slug')} hint={t('orgs.settings.slugReadonly')}>
        {(p) => <Input {...p} dir="ltr" value={org.slug} readOnly disabled />}
      </Field>
      <Field id="set-name" label={t('orgs.fields.name')} error={fieldErrors.name}>
        {(p) => <Input {...p} required value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} />}
      </Field>
      <Field id="set-locale" label={t('orgs.fields.locale')} error={fieldErrors.locale}>
        {(p) => (
          <Select value={draft.locale} onValueChange={(v) => isLocale(v) && setDraft({ ...draft, locale: v })}>
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
      <Field id="set-timezone" label={t('orgs.fields.timezone')} hint={t('orgs.hints.timezone')} error={fieldErrors.timezone}>
        {(p) => <Input {...p} dir="ltr" required value={draft.timezone} onChange={(e) => setDraft({ ...draft, timezone: e.target.value })} />}
      </Field>
      {generalError && (
        <Alert variant="destructive" role="alert">
          <AlertDescription>{generalError}</AlertDescription>
        </Alert>
      )}
      <div>
        <Button type="submit" disabled={patch.isPending || !dirty}>
          {t('orgs.settings.save')}
        </Button>
      </div>
    </form>
  )
}
