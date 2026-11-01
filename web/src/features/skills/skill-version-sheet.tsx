import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import type { SkillVersionDto } from '@/api/generated/model'
import { useApproveVersion, useRetireVersion, useSkill } from '@/api/hooks/skills'
import { Ltr } from '@/components/ltr'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { formatDateTime } from '@/lib/format'
import { problemMessage } from '@/lib/problem-message'
import { useLocale } from '@/lib/use-locale'

const versionVariant = (s: string): 'default' | 'secondary' | 'outline' => (s === 'approved' ? 'default' : s === 'draft' ? 'secondary' : 'outline')

/** One skill with its versions: read the body and the lint result, then approve (acknowledging flags) or retire. */
export function SkillVersionSheet({ orgId, skillId, onClose }: { orgId: string; skillId: string | null; onClose: () => void }) {
  const { t } = useTranslation()
  const q = useSkill(orgId, skillId)
  return (
    <Sheet open={!!skillId} onOpenChange={(o) => !o && onClose()}>
      <SheetContent side="end" className="w-full overflow-y-auto sm:max-w-2xl" data-testid="skill-sheet">
        <SheetHeader>
          <SheetTitle>{q.data?.skill.title ?? t('skills.title')}</SheetTitle>
          <SheetDescription>{t('skills.sheetHelp')}</SheetDescription>
        </SheetHeader>
        <div className="flex flex-col gap-6 px-4 pb-6">
          {q.isPending && !!skillId && <LoadingBlock />}
          {q.isError && <LoadFailed title={t('skills.title')} error={q.error} onRetry={() => q.refetch()} />}
          {q.data && <SkillBody orgId={orgId} revision={q.data.skill.revision} versions={q.data.versions} updateAvailable={q.data.update_available} />}
        </div>
      </SheetContent>
    </Sheet>
  )
}

function SkillBody({ orgId, revision, versions, updateAvailable }: { orgId: string; revision: number; versions: SkillVersionDto[]; updateAvailable: boolean }) {
  const { t } = useTranslation()
  const locale = useLocale()
  const sorted = [...versions].sort((a, b) => b.number - a.number)
  const [picked, setPicked] = useState<string | null>(null)
  const v = sorted.find((x) => x.id === picked) ?? sorted[0]
  const approve = useApproveVersion(orgId, revision)
  const retire = useRetireVersion(orgId, revision)
  const [acked, setAcked] = useState<string[]>([])
  if (!v) return null
  const blocking = v.lint.flags.filter((f) => f.blocking)
  const soft = [...new Set(v.lint.flags.filter((f) => !f.blocking).map((f) => f.code))]
  const canApprove = v.state === 'draft' && blocking.length === 0 && soft.every((c) => acked.includes(c))
  const fail = (e: unknown) => toast.error(problemMessage(t, e))
  return (
    <>
      {updateAvailable && <p className="text-sm text-muted-foreground">{t('skills.updateAvailable')}</p>}
      <ul className="flex flex-wrap gap-2" aria-label={t('skills.versions')}>
        {sorted.map((x) => (
          <li key={x.id}>
            <Button size="sm" variant={x.id === v.id ? 'secondary' : 'outline'} onClick={() => { setPicked(x.id); setAcked([]) }}>
              #{x.number} · <Ltr>{x.semver}</Ltr> · {t(`skills.state.${x.state}`)}
            </Button>
          </li>
        ))}
      </ul>

      <section className="flex flex-col gap-2" aria-label={t('skills.details')}>
        <div className="flex flex-wrap items-center gap-2">
          <Badge variant={versionVariant(v.state)} data-skill-state={v.state}>{t(`skills.state.${v.state}`)}</Badge>
          {v.roles.map((r) => (
            <Badge key={r} variant="outline">{t(`skills.roles.${r}`)}</Badge>
          ))}
        </div>
        <p className="text-sm">{v.description}</p>
        <p className="text-xs text-muted-foreground">
          {t('skills.license')} <Ltr>{v.license || '-'}</Ltr> · <Ltr className="break-all">{v.content_sha256.slice(0, 12)}</Ltr>
          {v.approved_at && ` · ${formatDateTime(v.approved_at, locale)}`}
        </p>
      </section>

      <section className="flex flex-col gap-2" aria-label={t('skills.lint.title')}>
        <h3 className="font-medium">{t('skills.lint.title')}</h3>
        {v.lint.flags.length === 0 && v.lint.warnings.length === 0 && <p className="text-sm text-muted-foreground">{t('skills.lint.clean')}</p>}
        <ul className="flex flex-col gap-1 text-sm">
          {v.lint.flags.map((f, i) => (
            <li key={`${f.code}-${i}`} className="flex flex-wrap items-center gap-2">
              <Badge variant={f.blocking ? 'destructive' : 'secondary'}>{t(f.blocking ? 'skills.lint.blocking' : 'skills.lint.flag')}</Badge>
              <span>{t(`skills.lint.codes.${f.code}`, { defaultValue: f.code })}</span>
              <span className="text-muted-foreground">{t('skills.lint.line', { line: f.line })}</span>
            </li>
          ))}
          {v.lint.warnings.map((w, i) => (
            <li key={`w-${i}`} className="text-muted-foreground">
              {t(`skills.lint.codes.${w.code}`, { defaultValue: w.code })} · {t('skills.lint.line', { line: w.line })}
            </li>
          ))}
        </ul>
        {blocking.length > 0 && (
          <Alert variant="destructive">
            <AlertDescription>{t('skills.lint.blockedHelp')}</AlertDescription>
          </Alert>
        )}
      </section>

      <section className="flex flex-col gap-2" aria-label={t('skills.body')}>
        <h3 className="font-medium">{t('skills.body')}</h3>
        <pre dir="ltr" className="max-h-72 overflow-auto whitespace-pre-wrap rounded-md bg-muted p-3 text-xs">{v.body}</pre>
      </section>

      {v.state === 'draft' && (
        <section className="flex flex-col gap-3" aria-label={t('skills.approve.title')}>
          <h3 className="font-medium">{t('skills.approve.title')}</h3>
          {soft.length > 0 && <p className="text-sm text-muted-foreground">{t('skills.approve.ackHelp')}</p>}
          {soft.map((c) => (
            <label key={c} className="flex items-center gap-2 text-sm">
              <Checkbox checked={acked.includes(c)} onCheckedChange={(on) => setAcked((s) => (on ? [...s, c] : s.filter((x) => x !== c)))} />
              {t('skills.approve.ack', { flag: t(`skills.lint.codes.${c}`, { defaultValue: c }) })}
            </label>
          ))}
          <div>
            <Button disabled={!canApprove || approve.isPending} onClick={() => approve.mutate({ orgId, skvId: v.id, data: { acknowledged_flags: acked } }, { onSuccess: () => toast.success(t('skills.approve.done')), onError: fail })}>
              {t('skills.approve.action')}
            </Button>
          </div>
        </section>
      )}
      {v.state === 'approved' && (
        <div>
          <Button variant="outline" disabled={retire.isPending} onClick={() => retire.mutate({ orgId, skvId: v.id }, { onSuccess: () => toast.success(t('skills.retire.done')), onError: fail })}>
            {t('skills.retire.action')}
          </Button>
        </div>
      )}
    </>
  )
}
