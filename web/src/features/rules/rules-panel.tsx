import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import type { PinnedRuleDto } from '@/api/generated/model'
import { useArchiveRule, useCreateRule, usePatchRule, usePinnedRules } from '@/api/hooks/rules'
import { ApiProblem } from '@/api/problem'
import { Field } from '@/components/field'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { problemMessage } from '@/lib/problem-message'

/** Pinned rules of the organization, or of one project. Critical rules are never cut from a context packet. */
export function RulesPanel({ orgId, projectId }: { orgId: string; projectId?: string }) {
  const { t } = useTranslation()
  const scope = projectId ? 'project' : 'org'
  const list = usePinnedRules(orgId, projectId ? { scope: 'project', project_id: projectId } : { scope: 'org' })
  const create = useCreateRule(orgId)
  const [title, setTitle] = useState('')
  const [body, setBody] = useState('')
  const [critical, setCritical] = useState(false)
  const [editing, setEditing] = useState<PinnedRuleDto | null>(null)
  const errors = create.error instanceof ApiProblem ? create.error.fieldErrors() : {}

  return (
    <div className="flex max-w-3xl flex-col gap-4">
      <div>
        <h2 className="text-lg font-semibold">{t(scope === 'org' ? 'rules.orgTitle' : 'rules.projectTitle')}</h2>
        <p className="text-sm text-muted-foreground">{t('rules.subtitle')}</p>
      </div>
      <form
        className="flex flex-col gap-3 rounded-lg border p-4"
        onSubmit={(e) => {
          e.preventDefault()
          create.mutate(
            { orgId, data: { scope, project_id: projectId ?? null, title, body, critical } },
            {
              onSuccess: () => {
                setTitle('')
                setBody('')
                setCritical(false)
                toast.success(t('rules.saved'))
              },
            },
          )
        }}
      >
        <Field id={`rule-title-${scope}`} label={t('rules.titleLabel')} error={errors.title}>
          {(p) => <Input {...p} dir="auto" maxLength={200} required value={title} onChange={(e) => setTitle(e.target.value)} />}
        </Field>
        <Field id={`rule-body-${scope}`} label={t('rules.bodyLabel')} error={errors.body} hint={t('rules.bodyHint')}>
          {(p) => <Textarea {...p} dir="auto" rows={4} required value={body} onChange={(e) => setBody(e.target.value)} />}
        </Field>
        <div className="flex items-center gap-2">
          <Switch id={`rule-critical-${scope}`} checked={critical} onCheckedChange={setCritical} aria-describedby={`rule-critical-help-${scope}`} />
          <label htmlFor={`rule-critical-${scope}`} className="text-sm">
            {t('rules.critical')}
          </label>
        </div>
        <p id={`rule-critical-help-${scope}`} className="text-xs text-muted-foreground">
          {t('rules.criticalHelp')}
        </p>
        {create.isError && !(create.error instanceof ApiProblem && create.error.code === 'validation_failed') && <p role="alert" className="text-sm text-destructive">{problemMessage(t, create.error)}</p>}
        <div>
          <Button type="submit" disabled={create.isPending || !title.trim() || !body.trim()}>
            {t('rules.add')}
          </Button>
        </div>
      </form>
      {list.isPending && <LoadingBlock rows={2} />}
      {list.isError && <LoadFailed title={t('rules.loadFailed')} error={list.error} onRetry={() => void list.refetch()} />}
      {list.data && list.data.length === 0 && (
        <Card>
          <CardContent className="py-4 text-sm text-muted-foreground">{t('rules.empty')}</CardContent>
        </Card>
      )}
      <ul className="flex flex-col gap-3" aria-label={t('rules.listLabel')}>
        {list.data?.map((r) => (
          <li key={r.id} className="rounded-lg border p-3 text-sm">
            <div className="flex flex-wrap items-center gap-2">
              <strong dir="auto">{r.title}</strong>
              {r.critical && <Badge variant="destructive">{t('rules.criticalBadge')}</Badge>}
              <Button size="sm" variant="ghost" className="ms-auto" onClick={() => setEditing(r)}>
                {t('common.edit')}
              </Button>
              <ArchiveButton orgId={orgId} rule={r} />
            </div>
            <p dir="auto" className="mt-1 whitespace-pre-wrap text-muted-foreground">
              {r.body}
            </p>
          </li>
        ))}
      </ul>
      {editing && <EditDialog orgId={orgId} rule={editing} onClose={() => setEditing(null)} />}
    </div>
  )
}

function ArchiveButton({ orgId, rule }: { orgId: string; rule: PinnedRuleDto }) {
  const { t } = useTranslation()
  const archive = useArchiveRule(orgId, rule.revision)
  return (
    <Button size="sm" variant="ghost" disabled={archive.isPending} onClick={() => archive.mutate({ orgId, rulId: rule.id }, { onError: (e) => toast.error(problemMessage(t, e)) })} aria-label={t('rules.archiveLabel', { title: rule.title })}>
      {t('common.archive')}
    </Button>
  )
}

function EditDialog({ orgId, rule, onClose }: { orgId: string; rule: PinnedRuleDto; onClose: () => void }) {
  const { t } = useTranslation()
  const [title, setTitle] = useState(rule.title)
  const [body, setBody] = useState(rule.body)
  const [critical, setCritical] = useState(rule.critical)
  const patch = usePatchRule(orgId, rule.revision)
  const conflict = patch.error instanceof ApiProblem && patch.error.code === 'revision_conflict'
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('rules.edit')}</DialogTitle>
          <DialogDescription>{t('rules.subtitle')}</DialogDescription>
        </DialogHeader>
        <Field id="edit-rule-title" label={t('rules.titleLabel')}>
          {(p) => <Input {...p} dir="auto" value={title} onChange={(e) => setTitle(e.target.value)} />}
        </Field>
        <Field id="edit-rule-body" label={t('rules.bodyLabel')}>
          {(p) => <Textarea {...p} dir="auto" rows={5} value={body} onChange={(e) => setBody(e.target.value)} />}
        </Field>
        <div className="flex items-center gap-2">
          <Switch id="edit-rule-critical" checked={critical} onCheckedChange={setCritical} />
          <label htmlFor="edit-rule-critical" className="text-sm">
            {t('rules.critical')}
          </label>
        </div>
        {patch.isError && (
          <p role="alert" className="text-sm text-destructive">
            {conflict ? t('rules.conflict') : problemMessage(t, patch.error)}
          </p>
        )}
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button
            disabled={patch.isPending || !title.trim() || !body.trim()}
            onClick={() => patch.mutate({ orgId, rulId: rule.id, data: { title, body, critical } }, { onSuccess: () => { toast.success(t('rules.saved')); onClose() } })}
          >
            {t('common.save')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
