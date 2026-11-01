import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useSkills } from '@/api/hooks/skills'
import { Ltr } from '@/components/ltr'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { ImportDialog } from './import-dialog'
import { SkillBindingsEditor } from './skill-bindings-editor'
import { SkillVersionSheet } from './skill-version-sheet'

/** Org skills: the library (built in and imported), the import flow and the org wide role bindings. */
export function SkillsTab({ orgId }: { orgId: string }) {
  const { t } = useTranslation()
  const list = useSkills(orgId)
  const [importing, setImporting] = useState(false)
  const [open, setOpen] = useState<string | null>(null)
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 className="text-lg font-semibold">{t('skills.title')}</h2>
          <p className="text-sm text-muted-foreground">{t('skills.subtitle')}</p>
        </div>
        <Button onClick={() => setImporting(true)}>{t('skills.import.open')}</Button>
      </div>
      {list.isPending && <LoadingBlock />}
      {list.isError && <LoadFailed title={t('skills.title')} error={list.error} onRetry={() => list.refetch()} />}
      {list.data && (
        <ul className="grid gap-3 md:grid-cols-2" aria-label={t('skills.title')}>
          {list.data.map((s) => (
            <li key={s.id}>
              <Card>
                <CardContent className="flex flex-col gap-2">
                  <div className="flex flex-wrap items-start justify-between gap-2">
                    <div className="min-w-0">
                      <h3 className="font-medium">{s.title}</h3>
                      <Ltr className="text-xs text-muted-foreground">{s.slug}</Ltr>
                    </div>
                    <Badge variant={s.approved_version_id ? 'default' : 'secondary'}>{t(s.approved_version_id ? 'skills.state.approved' : 'skills.state.draft')}</Badge>
                  </div>
                  <p className="text-sm text-muted-foreground">{s.description}</p>
                  <div className="flex flex-wrap gap-1">
                    <Badge variant="outline">{t(`skills.origin.${s.origin}`)}</Badge>
                    {s.semver && <Badge variant="outline"><Ltr>{s.semver}</Ltr></Badge>}
                    {s.roles.map((r) => (
                      <Badge key={r} variant="outline">{t(`skills.roles.${r}`)}</Badge>
                    ))}
                  </div>
                  <div>
                    <Button size="sm" variant="outline" onClick={() => setOpen(s.id)}>{t('skills.review')}</Button>
                  </div>
                </CardContent>
              </Card>
            </li>
          ))}
        </ul>
      )}
      <SkillBindingsEditor orgId={orgId} scopeKey="org" />
      <ImportDialog orgId={orgId} open={importing} onOpenChange={setImporting} onImported={setOpen} />
      <SkillVersionSheet orgId={orgId} skillId={open} onClose={() => setOpen(null)} />
    </div>
  )
}
