import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { TelescopeIcon } from 'lucide-react'
import { projectRows, useProjects } from '@/api/hooks/projects'
import { Ltr } from '@/components/ltr'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { Button } from '@/components/ui/button'
import { DiscoverPage } from '@/features/projects/discover-page'
import { ProjectDetailPage } from '@/features/projects/project-detail-page'
import { ProjectStateBadge } from '@/features/projects/project-state-badge'

type View = { kind: 'list' } | { kind: 'discover' } | { kind: 'project'; id: string }

/** Wizard "Projects" step: the project list, Discover, and the embedded project editor (mapping, commands, contract). */
export function ProjectsStep({ orgId }: { orgId: string }) {
  const { t } = useTranslation()
  const [view, setView] = useState<View>({ kind: 'list' })
  const list = useProjects(orgId, '')
  const rows = projectRows(list.data)
  if (view.kind === 'discover') {
    return (
      <div className="flex flex-col gap-4">
        <DiscoverPage embedded />
        <Button variant="outline" className="self-start" onClick={() => setView({ kind: 'list' })}>
          {t('wizard.backToList')}
        </Button>
      </div>
    )
  }
  if (view.kind === 'project') {
    return (
      <div className="flex flex-col gap-4">
        <ProjectDetailPage embedded projectId={view.id} onDone={() => setView({ kind: 'list' })} />
      </div>
    )
  }
  return (
    <div className="flex flex-col gap-4">
      <Button className="self-start" onClick={() => setView({ kind: 'discover' })}>
        <TelescopeIcon />
        {t('projects.discover')}
      </Button>
      {list.isPending && <LoadingBlock />}
      {list.isError && <LoadFailed title={t('projects.loadFailed')} error={list.error} onRetry={() => void list.refetch()} />}
      {list.isSuccess && rows.length === 0 && <p className="text-sm text-muted-foreground">{t('projects.empty')}</p>}
      <ul className="flex flex-col divide-y rounded-lg border empty:hidden" aria-label={t('projects.title')}>
        {rows.map((p) => (
          <li key={p.id} className="flex flex-wrap items-center justify-between gap-2 p-3 text-sm">
            <span className="flex flex-col">
              <span className="font-medium">{p.name}</span>
              {p.primary_repo && <Ltr className="text-xs text-muted-foreground">{p.primary_repo.path}</Ltr>}
            </span>
            <span className="flex items-center gap-2">
              <ProjectStateBadge state={p.state} />
              <Button size="sm" variant="outline" onClick={() => setView({ kind: 'project', id: p.id })}>
                {t('wizard.configure')}
                <span className="sr-only"> {p.name}</span>
              </Button>
            </span>
          </li>
        ))}
      </ul>
      {list.hasNextPage && (
        <Button variant="outline" className="self-start" onClick={() => void list.fetchNextPage()}>
          {t('projects.loadMore')}
        </Button>
      )}
    </div>
  )
}
