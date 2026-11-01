import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useParams, useSearchParams } from 'react-router'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { CatalogTab } from './catalog-tab'
import { MaintenanceTab } from './maintenance-tab'
import { OrchestratorKeysTab } from './orchestrator-keys-tab'
import { ProfilesTab } from './profiles-tab'
import { SkillsTab } from '@/features/skills/skills-tab'

const TABS = ['profiles', 'catalog', 'orchestrator', 'skills', 'maintenance'] as const
type Tab = (typeof TABS)[number]
const isTab = (v: string | null): v is Tab => !!v && (TABS as readonly string[]).includes(v)

export function ModelsPage({ embedded = false, initialTab = 'profiles', onOpenProfile }: { embedded?: boolean; initialTab?: Tab; onOpenProfile?: (id: string) => void }) {
  const { t } = useTranslation()
  const { orgId = '' } = useParams()
  const [search, setSearch] = useSearchParams()
  const [local, setLocal] = useState<Tab>(initialTab)
  const urlTab = search.get('tab')
  const tab = embedded ? local : isTab(urlTab) ? urlTab : initialTab
  const change = (v: string) => {
    if (!isTab(v)) return
    if (embedded) setLocal(v)
    else setSearch({ tab: v }, { replace: true })
  }
  return (
    <div className="flex flex-col gap-6">
      {!embedded && (
        <div>
          <h1 className="text-2xl font-semibold">{t('models.title')}</h1>
          <p className="text-sm text-muted-foreground">{t('models.subtitle')}</p>
        </div>
      )}
      <Tabs value={tab} onValueChange={change}>
        <TabsList aria-label={t('models.sections')}>
          {TABS.map((k) => (
            <TabsTrigger key={k} value={k}>
              {t(`models.tabs.${k}`)}
            </TabsTrigger>
          ))}
        </TabsList>
        <TabsContent value="profiles" className="pt-3">
          <ProfilesTab orgId={orgId} onOpenProfile={onOpenProfile} />
        </TabsContent>
        <TabsContent value="catalog" className="pt-3">
          <CatalogTab orgId={orgId} />
        </TabsContent>
        <TabsContent value="orchestrator" className="pt-3">
          <OrchestratorKeysTab orgId={orgId} />
        </TabsContent>
        <TabsContent value="skills" className="pt-3">
          <SkillsTab orgId={orgId} />
        </TabsContent>
        <TabsContent value="maintenance" className="pt-3">
          <MaintenanceTab orgId={orgId} />
        </TabsContent>
      </Tabs>
    </div>
  )
}
