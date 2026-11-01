import { useTranslation } from 'react-i18next'
import { Link } from 'react-router'
import { projectRows, useProjects } from '@/api/hooks/projects'
import type { SetupWizardDto } from '@/api/generated/model'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { Button } from '@/components/ui/button'
import { ProjectStateBadge } from '@/features/projects/project-state-badge'

/** Wizard "Review" step: every project's state with a link to its readiness tab, and the steps still pending. */
export function ReviewPanel({ orgId, wizard, goStep }: { orgId: string; wizard: SetupWizardDto; goStep: (key: string) => void }) {
  const { t } = useTranslation()
  const projects = useProjects(orgId, '')
  const pending = wizard.steps.filter((s) => s.key !== 'review' && (s.status !== 'done' || s.attention))
  return (
    <div className="flex flex-col gap-6">
      <section className="flex flex-col gap-2">
        <h3 className="text-sm font-medium">{t('wizard.review.projects')}</h3>
        {projects.isPending && <LoadingBlock rows={2} />}
        {projects.isError && <LoadFailed title={t('projects.loadFailed')} error={projects.error} onRetry={() => void projects.refetch()} />}
        {projects.isSuccess && projectRows(projects.data).length === 0 && <p className="text-sm text-muted-foreground">{t('projects.empty')}</p>}
        <ul className="flex flex-col divide-y rounded-lg border empty:hidden">
          {projectRows(projects.data).map((p) => (
            <li key={p.id} className="flex flex-wrap items-center justify-between gap-2 p-3 text-sm">
              <span className="font-medium">{p.name}</span>
              <span className="flex items-center gap-2">
                <ProjectStateBadge state={p.state} />
                <span className="text-xs text-muted-foreground tabular-nums">{t('wizard.review.blockers', { count: p.blocker_count })}</span>
                <Button asChild size="sm" variant="outline">
                  <Link to={`/orgs/${orgId}/projects/${p.id}?tab=readiness`}>
                    {t('wizard.review.openReadiness')}
                    <span className="sr-only"> {p.name}</span>
                  </Link>
                </Button>
              </span>
            </li>
          ))}
        </ul>
        <p className="text-xs text-muted-foreground">{t('wizard.review.enableHelp')}</p>
      </section>
      <section className="flex flex-col gap-2">
        <h3 className="text-sm font-medium">{t('wizard.review.pending')}</h3>
        {pending.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t('wizard.review.allDone')}</p>
        ) : (
          <ul className="flex flex-col gap-2 text-sm">
            {pending.map((s) => (
              <li key={s.key} className="flex flex-wrap items-center gap-2">
                <span className="font-medium">{t(`wizard.steps.${s.key}.title`)}</span>
                <span className="text-muted-foreground">{t(`wizard.status.${s.attention ? 'attention' : s.status}`)}</span>
                <Button size="sm" variant="outline" onClick={() => goStep(s.key)}>
                  {t('wizard.actions.resume')}
                </Button>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  )
}
