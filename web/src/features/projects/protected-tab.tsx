import { IconButton } from '@/components/icon-button'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { PlusIcon, Trash2Icon } from 'lucide-react'
import { toast } from 'sonner'
import type { ProjectDto, ProtectedPathInput } from '@/api/generated/model'
import { usePutProtectedPaths } from '@/api/hooks/projects'
import { ApiProblem, ok } from '@/api/problem'
import { ConflictAlert } from '@/components/conflict-alert'
import { Ltr } from '@/components/ltr'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { conflictCurrent, sameJson } from '@/lib/conflict'
import { problemMessage } from '@/lib/problem-message'

const toRules = (p: ProjectDto): ProtectedPathInput[] => p.protected_paths.map((r) => ({ pattern: r.pattern, mode: r.mode, note: r.note ?? '' }))

export function ProtectedTab({ orgId, project }: { orgId: string; project: ProjectDto }) {
  const { t } = useTranslation()
  const [base, setBase] = useState(project)
  const [rules, setRules] = useState<ProtectedPathInput[]>(toRules(project))
  const [conflict, setConflict] = useState<ProjectDto | null>(null)
  const dirty = !sameJson(rules, toRules(base))
  if (!dirty && project.revision > base.revision) {
    setBase(project)
    setRules(toRules(project))
  }
  const put = usePutProtectedPaths(orgId, base.revision)
  const errors = put.error instanceof ApiProblem && put.error.code !== 'revision_conflict' ? put.error.fieldErrors() : {}
  const general = put.isError && !conflict && Object.keys(errors).length === 0 ? problemMessage(t, put.error) : undefined

  const save = () =>
    put.mutate(
      { orgId, prjId: project.id, data: { rules: rules.map((r) => ({ ...r, pattern: r.pattern.trim(), note: r.note?.trim() || null })) } },
      {
        onSuccess: (res) => {
          const saved = ok(res)
          setBase(saved)
          setRules(toRules(saved))
          setConflict(null)
          toast.success(t('protectedPaths.saved'))
        },
        onError: (err) => {
          const cur = conflictCurrent<ProjectDto>(err)
          if (cur) setConflict(cur)
        },
      },
    )
  const set = (i: number, patch: Partial<ProtectedPathInput>) => setRules(rules.map((r, j) => (j === i ? { ...r, ...patch } : r)))

  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between gap-2">
        <CardTitle>{t('protectedPaths.title')}</CardTitle>
        <Button size="sm" variant="outline" onClick={() => setRules([...rules, { pattern: '', mode: 'deny', note: '' }])}>
          <PlusIcon />
          {t('protectedPaths.add')}
        </Button>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <p className="max-w-prose text-sm text-muted-foreground">{t('protectedPaths.help')}</p>
        {conflict && (
          <ConflictAlert
            rows={[{ label: t('protectedPaths.title'), mine: String(rules.length), latest: String(conflict.protected_paths.length) }]}
            onLoadLatest={() => {
              setBase(conflict)
              setRules(toRules(conflict))
              setConflict(null)
              put.reset()
            }}
            onKeepMine={() => {
              setBase(conflict)
              setConflict(null)
              put.reset()
            }}
          />
        )}
        {rules.length === 0 && <p className="text-sm text-muted-foreground">{t('protectedPaths.empty')}</p>}
        <ul className="flex flex-col gap-3" aria-label={t('protectedPaths.title')}>
          {rules.map((r, i) => (
            <li key={i} className="flex flex-wrap items-start gap-2">
              <div className="flex flex-col gap-1">
                <Input aria-label={t('protectedPaths.pattern')} dir="ltr" className="w-64 font-mono text-xs" value={r.pattern} onChange={(e) => set(i, { pattern: e.target.value })} placeholder="src/secrets/**" aria-invalid={errors[`rules[${i}].pattern`] ? true : undefined} />
                {errors[`rules[${i}].pattern`] && <p className="text-xs text-destructive" role="alert"><Ltr className="font-sans">{errors[`rules[${i}].pattern`]}</Ltr></p>}
              </div>
              <Select value={r.mode} onValueChange={(v) => set(i, { mode: v as ProtectedPathInput['mode'] })}>
                <SelectTrigger aria-label={t('protectedPaths.mode')} className="w-40">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="deny">{t('protectedPaths.modes.deny')}</SelectItem>
                  <SelectItem value="review">{t('protectedPaths.modes.review')}</SelectItem>
                </SelectContent>
              </Select>
              <Input aria-label={t('protectedPaths.note')} className="w-64" value={r.note ?? ''} onChange={(e) => set(i, { note: e.target.value })} placeholder={t('protectedPaths.note')} />
              <IconButton size="icon" variant="ghost" aria-label={t('protectedPaths.remove', { pattern: r.pattern })} onClick={() => setRules(rules.filter((_, j) => j !== i))}>
                <Trash2Icon />
              </IconButton>
            </li>
          ))}
        </ul>
        {general && (
          <Alert variant="destructive" role="alert">
            <AlertDescription>{general}</AlertDescription>
          </Alert>
        )}
        <div className="flex gap-2">
          <Button onClick={save} disabled={!dirty || put.isPending}>
            {t('common.save')}
          </Button>
          {dirty && (
            <Button variant="ghost" onClick={() => setRules(toRules(base))}>
              {t('policy.discard')}
            </Button>
          )}
        </div>
      </CardContent>
    </Card>
  )
}
