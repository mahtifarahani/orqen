import { useMemo, useState } from 'react'
import { ArrowDownIcon, ArrowUpIcon, Trash2Icon } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import type { SkillBindingInput, SkillDto } from '@/api/generated/model'
import { newIdempotencyKey } from '@/api/idempotency'
import { usePutBindings, useRecommendedBindings, useResolvedSkills, useSkillBindings, useSkills } from '@/api/hooks/skills'
import { IconButton } from '@/components/icon-button'
import { Ltr } from '@/components/ltr'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { formatNumber } from '@/lib/format'
import { problemMessage } from '@/lib/problem-message'
import { useLocale } from '@/lib/use-locale'

const ROLES = ['architect', 'worker', 'verifier', 'reviewer', 'researcher', 'writer'] as const
type Role = (typeof ROLES)[number]
const selectClass = 'h-9 min-w-0 rounded-md border bg-background px-2 text-sm'

/**
 * Which approved skills each role gets, for one scope: `org` or `project:<id>`. A project set overrides the org set.
 * Saving replaces the whole set (If-Match = the set revision).
 */
export function SkillBindingsEditor({ orgId, scopeKey, projectId }: { orgId: string; scopeKey: string; projectId?: string }) {
  const { t } = useTranslation()
  const locale = useLocale()
  const sets = useSkillBindings(orgId, scopeKey)
  const skills = useSkills(orgId)
  const [draft, setDraft] = useState<{ base: number; rows: SkillBindingInput[] } | null>(null)
  const [role, setRole] = useState<Role>('worker')
  const [skillId, setSkillId] = useState('')
  const put = usePutBindings(orgId, sets.data?.revision ?? 0)
  const [recKey, setRecKey] = useState(newIdempotencyKey)
  const recommended = useRecommendedBindings(orgId, recKey)
  const resolved = useResolvedSkills(orgId, projectId, role)
  const bySkill = useMemo(() => new Map((skills.data ?? []).map((s) => [s.id, s])), [skills.data])

  if (sets.isPending || skills.isPending) return <LoadingBlock rows={2} />
  if (sets.isError) return <LoadFailed title={t('skills.bindings.title')} error={sets.error} onRetry={() => sets.refetch()} />
  if (skills.isError) return <LoadFailed title={t('skills.bindings.title')} error={skills.error} onRetry={() => skills.refetch()} />

  // The local draft is dropped once the server revision moved on (a save, or another tab).
  const rows = draft && draft.base === sets.data.revision ? draft.rows : sets.data.bindings
  const dirty = !!draft && draft.base === sets.data.revision
  const edit = (next: SkillBindingInput[]) => setDraft({ base: sets.data.revision, rows: next })
  const approvable: SkillDto[] = skills.data.filter((s) => s.approved_version_id)
  const name = (id: string) => bySkill.get(id)?.title ?? id

  const roleRows = (r: Role) => rows.filter((b) => b.role === r).sort((a, b) => a.position - b.position)
  const move = (r: Role, id: string, dir: -1 | 1) => {
    const list = roleRows(r)
    const i = list.findIndex((b) => b.skill_id === id)
    const j = i + dir
    if (i < 0 || j < 0 || j >= list.length) return
    ;[list[i], list[j]] = [list[j], list[i]]
    edit([...rows.filter((b) => b.role !== r), ...list.map((b, k) => ({ ...b, position: k }))])
  }
  const add = () => {
    const s = bySkill.get(skillId)
    if (!s?.approved_version_id || rows.some((b) => b.role === role && b.skill_id === s.id)) return
    edit([...rows, { role, skill_id: s.id, version_id: s.approved_version_id, enabled: true, position: roleRows(role).length }])
    setSkillId('')
  }
  const fail = (e: unknown) => toast.error(problemMessage(t, e))
  const save = () => put.mutate({ orgId, data: { scope_key: scopeKey, bindings: rows } }, { onSuccess: () => { setDraft(null); toast.success(t('skills.bindings.saved')) }, onError: fail })
  const res = resolved.data

  return (
    <section className="flex flex-col gap-4" aria-label={t('skills.bindings.title')}>
      <div>
        <h3 className="font-medium">{t(scopeKey === 'org' ? 'skills.bindings.orgTitle' : 'skills.bindings.projectTitle')}</h3>
        <p className="text-sm text-muted-foreground">{t(scopeKey === 'org' ? 'skills.bindings.orgHelp' : 'skills.bindings.projectHelp')}</p>
      </div>
      {scopeKey === 'org' && rows.length === 0 && (
        <div>
          <Button variant="outline" disabled={recommended.isPending} onClick={() => recommended.mutate({ orgId }, { onSuccess: () => { setRecKey(newIdempotencyKey()); toast.success(t('skills.bindings.recommendedDone')) }, onError: fail })}>
            {t('skills.bindings.recommended')}
          </Button>
        </div>
      )}
      {ROLES.map((r) => {
        const list = roleRows(r)
        if (list.length === 0) return null
        return (
          <div key={r} className="flex flex-col gap-1">
            <h4 className="text-sm font-medium">{t(`skills.roles.${r}`)}</h4>
            <ul className="flex flex-col gap-1">
              {list.map((b, i) => (
                <li key={b.skill_id} className="flex flex-wrap items-center justify-between gap-2 rounded-md border p-2 text-sm">
                  <span className="min-w-0 truncate">{name(b.skill_id)}</span>
                  <span className="flex items-center gap-1">
                    <Switch checked={b.enabled} aria-label={t('skills.bindings.enabled', { name: name(b.skill_id) })} onCheckedChange={(on) => edit(rows.map((x) => (x === b ? { ...x, enabled: on } : x)))} />
                    <IconButton variant="ghost" size="icon-sm" aria-label={t('skills.bindings.up', { name: name(b.skill_id) })} disabled={i === 0} onClick={() => move(r, b.skill_id, -1)}>
                      <ArrowUpIcon />
                    </IconButton>
                    <IconButton variant="ghost" size="icon-sm" aria-label={t('skills.bindings.down', { name: name(b.skill_id) })} disabled={i === list.length - 1} onClick={() => move(r, b.skill_id, 1)}>
                      <ArrowDownIcon />
                    </IconButton>
                    <IconButton variant="ghost" size="icon-sm" aria-label={t('skills.bindings.remove', { name: name(b.skill_id) })} onClick={() => edit(rows.filter((x) => x !== b))}>
                      <Trash2Icon />
                    </IconButton>
                  </span>
                </li>
              ))}
            </ul>
          </div>
        )
      })}
      {rows.length === 0 && <p className="text-sm text-muted-foreground">{t('skills.bindings.none')}</p>}

      <div className="flex flex-wrap items-end gap-2">
        <label className="flex flex-col gap-1 text-sm">
          {t('skills.bindings.role')}
          <select className={selectClass} value={role} onChange={(e) => setRole(e.target.value as Role)}>
            {ROLES.map((r) => (
              <option key={r} value={r}>{t(`skills.roles.${r}`)}</option>
            ))}
          </select>
        </label>
        <label className="flex min-w-0 flex-col gap-1 text-sm">
          {t('skills.bindings.skill')}
          <select className={selectClass} value={skillId} onChange={(e) => setSkillId(e.target.value)}>
            <option value="">{t('skills.bindings.pick')}</option>
            {approvable.map((s) => (
              <option key={s.id} value={s.id}>{s.title}</option>
            ))}
          </select>
        </label>
        <Button variant="outline" disabled={!skillId} onClick={add}>
          {t('skills.bindings.add')}
        </Button>
      </div>

      {put.isError && (
        <Alert variant="destructive" role="alert">
          <AlertDescription>{problemMessage(t, put.error)}</AlertDescription>
        </Alert>
      )}
      <div className="flex flex-wrap gap-2">
        <Button disabled={!dirty || put.isPending} onClick={save}>{t('common.save')}</Button>
        <Button variant="ghost" disabled={!dirty} onClick={() => setDraft(null)}>{t('skills.bindings.discard')}</Button>
      </div>

      {projectId && res && (
        <div className="flex flex-col gap-1 text-sm" role="status" aria-label={t('skills.bindings.resolved')}>
          <h4 className="font-medium">{t('skills.bindings.resolved')} ({t(`skills.roles.${role}`)})</h4>
          <p className="text-muted-foreground">{t('skills.bindings.budget', { bytes: formatNumber(res.bytes, locale), budget: formatNumber(res.budget, locale) })}</p>
          <ul className="flex flex-col gap-0.5">
            {res.included.map((i) => (
              <li key={i.version_id}>{i.slug} <Ltr>{i.semver}</Ltr></li>
            ))}
            {res.excluded.map((x) => (
              <li key={x.slug} className="text-muted-foreground">{x.slug}: {t(`skills.excluded.${x.reason}`, { defaultValue: x.reason })}</li>
            ))}
          </ul>
        </div>
      )}
    </section>
  )
}
