import { useTranslation } from 'react-i18next'
import type { RunSkillRoleDto } from '@/api/generated/model'
import { useRunSkills } from '@/api/hooks/skills'
import { Ltr } from '@/components/ltr'
import { Badge } from '@/components/ui/badge'

/** The skills pinned for a run, per role and round, so a later edit of a skill never changes a started run. */
export function RunSkillsManifest({ orgId, runId }: { orgId: string; runId: string }) {
  const { t } = useTranslation()
  const q = useRunSkills(orgId, runId)
  if (!q.data) return null
  const roles = Object.entries(q.data.roles as Record<string, RunSkillRoleDto>).filter(([, r]) => r.round0.length + r.fix.length + r.excluded0.length + r.excluded_fix.length > 0)
  if (roles.length === 0) return null
  return (
    <section className="flex flex-col gap-2" aria-label={t('skills.manifest.title')}>
      <h3 className="font-medium">{t('skills.manifest.title')}</h3>
      {q.data.fingerprint && (
        <p className="text-xs text-muted-foreground">
          {t('skills.manifest.fingerprint')} <Ltr className="break-all">{q.data.fingerprint.slice(0, 16)}</Ltr>
        </p>
      )}
      <ul className="flex flex-col gap-2">
        {roles.map(([role, r]) => (
          <li key={role} className="rounded-md border p-2 text-sm">
            <span className="font-medium">{t(`skills.roles.${role}`, { defaultValue: role })}</span>
            <div className="mt-1 flex flex-wrap gap-1">
              {r.round0.map((p) => (
                <Badge key={`0-${p.version_id}`} variant="outline">{p.slug} <Ltr>{p.semver}</Ltr></Badge>
              ))}
              {r.fix.map((p) => (
                <Badge key={`f-${p.version_id}`} variant="secondary">{t('skills.manifest.fix')} {p.slug} <Ltr>{p.semver}</Ltr></Badge>
              ))}
              {[...r.excluded0, ...r.excluded_fix].map((x, i) => (
                <Badge key={`x-${x.slug}-${i}`} variant="destructive">{x.slug}: {t(`skills.excluded.${x.reason}`, { defaultValue: x.reason })}</Badge>
              ))}
            </div>
          </li>
        ))}
      </ul>
    </section>
  )
}
