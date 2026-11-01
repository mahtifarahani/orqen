import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router'
import { toast } from 'sonner'
import type { ProjectDto } from '@/api/generated/model'
import { useProfiles, useProfileVersions, useProfileVersion } from '@/api/hooks/models'
import { usePutProfileBinding } from '@/api/hooks/projects'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { Ltr } from '@/components/ltr'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { IndependenceBadge } from '@/features/models/independence-badge'
import { formatDateTime } from '@/lib/format'
import { problemMessage } from '@/lib/problem-message'
import { useLocale } from '@/lib/use-locale'

export function ProfileTab({ orgId, project }: { orgId: string; project: ProjectDto }) {
  const { t } = useTranslation()
  const locale = useLocale()
  const profiles = useProfiles(orgId)
  const binding = project.profile_binding
  const [profileId, setProfileId] = useState<string | undefined>(binding?.profile_id)
  const versions = useProfileVersions(orgId, profileId)
  const published = (versions.data ?? []).filter((v) => v.state === 'published')
  const [versionId, setVersionId] = useState<string | undefined>(binding?.version_id)
  const bound = useProfileVersion(orgId, binding?.version_id)
  const put = usePutProfileBinding(orgId, project.revision)
  const bind = (id: string | null) =>
    put.mutate(
      { orgId, prjId: project.id, data: { version_id: id } },
      { onSuccess: () => toast.success(id ? t('profileBinding.bound') : t('profileBinding.unbound')), onError: (e) => toast.error(problemMessage(t, e)) },
    )

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('profileBinding.title')}</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <p className="max-w-prose text-sm text-muted-foreground">{t('profileBinding.help')}</p>
        {binding ? (
          <div className="flex flex-col gap-2 rounded-lg border p-3 text-sm">
            <div className="flex flex-wrap items-center gap-2">
              <span className="font-medium">{t('profileBinding.current')}</span>
              {bound.data ? <Ltr>{bound.data.catherd_name}</Ltr> : <Ltr>{binding.version_id}</Ltr>}
              {bound.data && <Badge variant="secondary">v{bound.data.number}</Badge>}
              {bound.data?.validation && <IndependenceBadge independence={bound.data.validation.independence} />}
            </div>
            <span className="text-xs text-muted-foreground">{t('profileBinding.boundAt', { time: formatDateTime(binding.bound_at, locale) })}</span>
            <div>
              <Button size="sm" variant="outline" onClick={() => bind(null)} disabled={put.isPending}>
                {t('profileBinding.unbind')}
              </Button>
            </div>
          </div>
        ) : (
          <p className="text-sm text-muted-foreground">{t('profileBinding.none')}</p>
        )}

        {profiles.isPending && <LoadingBlock rows={1} />}
        {profiles.isError && <LoadFailed title={t('models.loadFailed')} error={profiles.error} onRetry={() => void profiles.refetch()} />}
        {profiles.isSuccess && profiles.data.length === 0 && (
          <div className="flex flex-col items-start gap-2">
            <p className="text-sm text-muted-foreground">{t('profileBinding.noProfiles')}</p>
            <Button asChild size="sm">
              <Link to={`/orgs/${orgId}/models`}>{t('profileBinding.toModels')}</Link>
            </Button>
          </div>
        )}
        {profiles.isSuccess && profiles.data.length > 0 && (
          <div className="grid max-w-xl gap-4 sm:grid-cols-2">
            <div className="flex flex-col gap-2">
              <Label htmlFor="bind-profile">{t('profileBinding.profile')}</Label>
              <Select
                value={profileId}
                onValueChange={(v) => {
                  setProfileId(v)
                  setVersionId(undefined)
                }}
              >
                <SelectTrigger id="bind-profile" className="w-full">
                  <SelectValue placeholder={t('profileBinding.chooseProfile')} />
                </SelectTrigger>
                <SelectContent>
                  {profiles.data.map((p) => (
                    <SelectItem key={p.id} value={p.id}>
                      {p.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor="bind-version">{t('profileBinding.version')}</Label>
              <Select value={versionId} onValueChange={setVersionId} disabled={!profileId}>
                <SelectTrigger id="bind-version" className="w-full">
                  <SelectValue placeholder={t('profileBinding.chooseVersion')} />
                </SelectTrigger>
                <SelectContent>
                  {published.map((v) => (
                    <SelectItem key={v.id} value={v.id}>
                      v{v.number} · {v.catherd_name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              {profileId && versions.isSuccess && published.length === 0 && <p className="text-xs text-muted-foreground">{t('profileBinding.noPublished')}</p>}
            </div>
            <div className="sm:col-span-2">
              <Button disabled={!versionId || versionId === binding?.version_id || put.isPending} onClick={() => versionId && bind(versionId)}>
                {t('profileBinding.bind')}
              </Button>
            </div>
          </div>
        )}
      </CardContent>
    </Card>
  )
}
