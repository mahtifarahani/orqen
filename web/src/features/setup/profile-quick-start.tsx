import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useProfileVersions } from '@/api/hooks/models'
import { projectRows, usePutProfileBinding, useProjects } from '@/api/hooks/projects'
import type { ProjectSummaryDto } from '@/api/generated/model'
import { Ltr } from '@/components/ltr'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { ProfileEditorPage } from '@/features/models/profile-editor-page'
import { ProfilesTab } from '@/features/models/profiles-tab'
import { problemMessage } from '@/lib/problem-message'

/** Wizard "Model profile" step: create or import, edit/validate/publish in the embedded editor, then bind to projects. */
export function ProfileQuickStart({ orgId }: { orgId: string }) {
  const { t } = useTranslation()
  const [profileId, setProfileId] = useState<string | null>(null)
  if (!profileId) return <ProfilesTab orgId={orgId} onOpenProfile={setProfileId} />
  return (
    <div className="flex flex-col gap-6">
      <Button variant="ghost" className="self-start" onClick={() => setProfileId(null)}>
        {t('wizard.backToProfiles')}
      </Button>
      <ProfileEditorPage embedded profileId={profileId} />
      <BindPanel orgId={orgId} profileId={profileId} />
    </div>
  )
}

function BindPanel({ orgId, profileId }: { orgId: string; profileId: string }) {
  const { t } = useTranslation()
  const versions = useProfileVersions(orgId, profileId)
  const projects = useProjects(orgId, '')
  const latest = (versions.data ?? []).find((v) => v.state === 'published')
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('wizard.bind.title')}</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {!latest && <p className="text-sm text-muted-foreground">{t('wizard.bind.needsPublished')}</p>}
        {latest && (
          <>
            <p className="text-sm text-muted-foreground">
              {t('wizard.bind.help')} <Ltr>{latest.catherd_name}</Ltr>
            </p>
            {projectRows(projects.data).length === 0 && <p className="text-sm">{t('wizard.bind.noProjects')}</p>}
            <ul className="flex flex-col divide-y">
              {projectRows(projects.data).map((p) => (
                <BindRow key={p.id} orgId={orgId} project={p} versionId={latest.id} />
              ))}
            </ul>
          </>
        )}
      </CardContent>
    </Card>
  )
}

function BindRow({ orgId, project, versionId }: { orgId: string; project: ProjectSummaryDto; versionId: string }) {
  const { t } = useTranslation()
  const bind = usePutProfileBinding(orgId, project.revision)
  return (
    <li className="flex items-center justify-between gap-2 py-2 text-sm">
      <span>{project.name}</span>
      <Button
        size="sm"
        variant="outline"
        disabled={bind.isPending}
        onClick={() => bind.mutate({ orgId, prjId: project.id, data: { version_id: versionId } }, { onSuccess: () => toast.success(t('profileBinding.bound')), onError: (e) => toast.error(problemMessage(t, e)) })}
      >
        {t('profileBinding.bind')}
        <span className="sr-only"> {project.name}</span>
      </Button>
    </li>
  )
}
