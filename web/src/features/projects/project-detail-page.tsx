import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link, useParams, useSearchParams } from 'react-router'
import { ArrowLeftIcon } from 'lucide-react'
import { useProject } from '@/api/hooks/projects'
import { Ltr } from '@/components/ltr'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { Button } from '@/components/ui/button'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { ProjectPolicyPanel } from '@/features/policy/policy-panel'
import { CommandsTab } from './commands-tab'
import { ContractTab } from './contract-tab'
import { IntakeTab } from './intake-tab'
import { MappingTab } from './mapping-tab'
import { ProjectRulesTab } from './rules-tab'
import { ProfileTab } from './profile-tab'
import { ProjectStateBadge } from './project-state-badge'
import { ProjectSkillsTab } from './skills-tab'
import { ProtectedTab } from './protected-tab'
import { ReadinessTab } from './readiness-tab'
import { isProjectTab as isTab, PROJECT_TABS, type ProjectTab } from './project-tabs'


interface Props {
  /** Embedded in the setup wizard: no page header, no URL tab state, project chosen by the caller. */
  embedded?: boolean
  projectId?: string
  onDone?: () => void
}

export function ProjectDetailPage({ embedded = false, projectId: projectIdProp, onDone }: Props) {
  const { t } = useTranslation()
  const params = useParams()
  const orgId = params.orgId ?? ''
  const projectId = projectIdProp ?? params.projectId ?? ''
  const project = useProject(orgId, projectId)
  const [search, setSearch] = useSearchParams()
  const [localTab, setLocalTab] = useState<ProjectTab>('readiness')
  const urlTab = search.get('tab')
  const tab: ProjectTab = embedded ? localTab : isTab(urlTab) ? urlTab : 'readiness'
  const setTab = (next: string) => {
    if (!isTab(next)) return
    if (embedded) setLocalTab(next)
    else setSearch({ tab: next }, { replace: true })
  }
  const primary = project.data?.mapping.repos.find((r) => r.role === 'primary')

  return (
    <div className="flex flex-col gap-6">
      {!embedded && (
        <Button asChild variant="ghost" className="self-start">
          <Link to={`/orgs/${orgId}/projects`}>
            <ArrowLeftIcon className="rtl:-scale-x-100" />
            {t('projects.detail.back')}
          </Link>
        </Button>
      )}
      {project.isPending && <LoadingBlock rows={1} />}
      {project.isError && <LoadFailed title={t('projects.detail.loadFailed')} error={project.error} onRetry={() => void project.refetch()} />}
      {project.data && (
        <>
          <div className="flex flex-wrap items-center gap-3">
            {embedded ? <h2 className="text-lg font-semibold">{project.data.name}</h2> : <h1 className="text-2xl font-semibold">{project.data.name}</h1>}
            <ProjectStateBadge state={project.data.state} />
          </div>
          {primary && (
            <p className="-mt-4 text-sm text-muted-foreground">
              <Ltr>{primary.path_snapshot}</Ltr>
            </p>
          )}
          <Tabs value={tab} onValueChange={setTab}>
            <div className="overflow-x-auto">
              <TabsList aria-label={t('projects.detail.tabsLabel')}>
                {PROJECT_TABS.map((k) => (
                  <TabsTrigger key={k} value={k}>
                    {t(`projects.tabs.${k}`)}
                  </TabsTrigger>
                ))}
              </TabsList>
            </div>
            <TabsContent value="readiness">
              <ReadinessTab orgId={orgId} project={project.data} goTab={setTab} />
            </TabsContent>
            <TabsContent value="mapping">
              <MappingTab orgId={orgId} project={project.data} />
            </TabsContent>
            <TabsContent value="commands">
              <CommandsTab orgId={orgId} project={project.data} />
            </TabsContent>
            <TabsContent value="protected">
              <ProtectedTab orgId={orgId} project={project.data} />
            </TabsContent>
            <TabsContent value="contract">
              <ContractTab orgId={orgId} project={project.data} goTab={setTab} />
            </TabsContent>
            <TabsContent value="policy">
              <ProjectPolicyPanel orgId={orgId} projectId={project.data.id} />
            </TabsContent>
            <TabsContent value="profile">
              <ProfileTab orgId={orgId} project={project.data} />
            </TabsContent>
            <TabsContent value="intake">
              <IntakeTab orgId={orgId} project={project.data} />
            </TabsContent>
            <TabsContent value="rules">
              <ProjectRulesTab orgId={orgId} project={project.data} />
            </TabsContent>
            <TabsContent value="skills">
              <ProjectSkillsTab orgId={orgId} project={project.data} />
            </TabsContent>
          </Tabs>
          {embedded && onDone && (
            <div>
              <Button variant="outline" onClick={onDone}>
                {t('projects.detail.done')}
              </Button>
            </div>
          )}
        </>
      )}
    </div>
  )
}
