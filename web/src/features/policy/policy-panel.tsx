import { IconButton } from '@/components/icon-button'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useSearchParams } from 'react-router'
import { LockIcon, RotateCcwIcon } from 'lucide-react'
import { toast } from 'sonner'
import type { PolicyDraftDto, PolicyEntryDto, PolicyKeyDto } from '@/api/generated/model'
import { useOrgPolicy, usePolicySchema, useProjectPolicy, type PolicyScope } from '@/api/hooks/policy'
import { newIdempotencyKey } from '@/api/idempotency'
import { ApiProblem, ok } from '@/api/problem'
import { ConflictAlert } from '@/components/conflict-alert'
import { Ltr } from '@/components/ltr'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { conflictCurrent, sameJson } from '@/lib/conflict'
import { formatDateTime } from '@/lib/format'
import { problemMessage } from '@/lib/problem-message'
import { useLocale } from '@/lib/use-locale'

type Overrides = Record<string, unknown>
const slug = (key: string) => key.replace(/\./g, '_')

/** Org policy editor. `onPublished` is called after a publish (the setup wizard moves on). */
export function OrgPolicyPanel({ orgId, onPublished }: { orgId: string; onPublished?: () => void }) {
  const scope = useOrgPolicy(orgId)
  // `?prefill=<key>&value=<v>` (the Orchestrator tab's host change) opens the draft with that override filled in.
  const [search] = useSearchParams()
  const key = search.get('prefill')
  const value = search.get('value')
  const prefill = key && value !== null ? { key, value } : undefined
  return <PolicyPanel orgId={orgId} scope={scope} level="org" onPublished={onPublished} prefill={prefill} />
}

export function ProjectPolicyPanel({ orgId, projectId }: { orgId: string; projectId: string }) {
  const scope = useProjectPolicy(orgId, projectId)
  return <PolicyPanel orgId={orgId} scope={scope} level="project" />
}

function PolicyPanel({ orgId, scope, level, onPublished, prefill }: { orgId: string; scope: PolicyScope; level: 'org' | 'project'; onPublished?: () => void; prefill?: { key: string; value: string } }) {
  const { t } = useTranslation()
  const schema = usePolicySchema(orgId)
  const { draft, effective, versions } = scope
  if (schema.isPending || draft.isPending || effective.isPending) return <LoadingBlock />
  const failed = schema.error ?? draft.error ?? effective.error
  if (failed || !schema.data || !draft.data || !effective.data) {
    return (
      <LoadFailed
        title={t('policy.loadFailed')}
        error={failed}
        onRetry={() => {
          void schema.refetch()
          void draft.refetch()
          void effective.refetch()
        }}
      />
    )
  }
  return (
    <PolicyEditor
      scope={scope}
      level={level}
      schema={schema.data}
      draft={draft.data}
      entries={effective.data.items}
      versionNumbers={(versions.data ?? []).map((v) => ({ id: v.id, number: v.number, created_at: v.created_at }))}
      onPublished={onPublished}
      prefill={prefill}
    />
  )
}

interface EditorProps {
  scope: PolicyScope
  level: 'org' | 'project'
  schema: PolicyKeyDto[]
  draft: PolicyDraftDto
  entries: PolicyEntryDto[]
  versionNumbers: { id: string; number: number; created_at: string }[]
  onPublished?: () => void
  prefill?: { key: string; value: string }
}

function PolicyEditor({ scope, level, schema, draft, entries, versionNumbers, onPublished, prefill }: EditorProps) {
  const { t } = useTranslation()
  const locale = useLocale()
  // `base` is the server draft the local overrides are built on; its revision goes into If-Match.
  const [base, setBase] = useState(draft)
  const known = prefill ? schema.find((k) => k.key === prefill.key && (k.enum ?? []).includes(prefill.value)) : undefined
  const [overrides, setOverrides] = useState<Overrides>({ ...(draft.overrides as Overrides), ...(known && prefill ? { [prefill.key]: prefill.value } : {}) })
  const [conflict, setConflict] = useState<PolicyDraftDto | null>(null)
  const [pubKey, setPubKey] = useState(newIdempotencyKey)
  const [publishNext, setPublishNext] = useState(false)
  const dirty = !sameJson(overrides, base.overrides)
  if (!dirty && draft.revision > base.revision) {
    setBase(draft)
    setOverrides(draft.overrides as Overrides)
  }
  const save = scope.useSave(base.revision)
  const publish = scope.usePublish(base.revision, pubKey)

  const runPublish = () =>
    publish.run({
      onSuccess: (res) => {
        const v = ok(res as never) as { number: number }
        toast.success(t('policy.published', { number: v.number }))
        setPubKey(newIdempotencyKey())
        onPublished?.()
      },
      onError: (err) => {
        const cur = conflictCurrent<PolicyDraftDto>(err)
        if (cur) setConflict(cur)
      },
    })
  const runSave = (next: Overrides, thenPublish: boolean) =>
    save.run(next, {
      onSuccess: (res) => {
        setBase(ok(res as never) as PolicyDraftDto)
        setConflict(null)
        if (thenPublish) setPublishNext(true)
        else toast.success(t('policy.saved'))
      },
      onError: (err) => {
        const cur = conflictCurrent<PolicyDraftDto>(err)
        if (cur) setConflict(cur)
      },
    })
  // Publishing right after saving waits for the render that carries the new revision into If-Match.
  // oxlint-disable-next-line react-hooks/exhaustive-deps -- runs after every render on purpose; it only acts once per flag
  useEffect(() => {
    if (publishNext && !save.isPending) {
      setPublishNext(false)
      runPublish()
    }
  })

  const byKey = new Map(entries.map((e) => [e.key, e]))
  const set = (key: string, v: unknown) => setOverrides({ ...overrides, [key]: v })
  const reset = (key: string) => {
    const { [key]: _drop, ...rest } = overrides
    void _drop
    setOverrides(rest)
  }
  const saveError = save.error ?? publish.error
  const fieldErrors = saveError instanceof ApiProblem && saveError.code !== 'revision_conflict' ? saveError.fieldErrors() : {}
  const generalError = saveError && !conflict && Object.keys(fieldErrors).length === 0 ? problemMessage(t, saveError) : undefined
  const rows = conflict
    ? Object.keys({ ...overrides, ...conflict.overrides })
        .filter((k) => !sameJson(overrides[k], (conflict.overrides as Overrides)[k]))
        .map((k) => ({ label: k, mine: JSON.stringify(overrides[k] ?? null), latest: JSON.stringify((conflict.overrides as Overrides)[k] ?? null) }))
    : []

  return (
    <div className="flex flex-col gap-4">
      {known && prefill && dirty && (
        <Alert role="status">
          <AlertDescription>{t('policy.prefilled', { key: prefill.key, value: prefill.value })}</AlertDescription>
        </Alert>
      )}
      <div className="flex flex-wrap items-center gap-2">
        {base.differs_from_published && <Badge variant="outline">{t('policy.differs')}</Badge>}
        {base.revision === 0 && <Badge variant="outline">{t('policy.noDraft')}</Badge>}
        {dirty && <Badge variant="outline">{t('policy.unsaved')}</Badge>}
        <span className="text-xs text-muted-foreground">
          {level === 'org' ? t('policy.orgHint') : t('policy.projectHint')}
        </span>
      </div>
      {conflict && (
        <ConflictAlert
          rows={rows}
          onLoadLatest={() => {
            setBase(conflict)
            setOverrides(conflict.overrides as Overrides)
            setConflict(null)
            save.reset()
            publish.reset()
          }}
          onKeepMine={() => {
            setBase(conflict)
            setConflict(null)
            save.reset()
            publish.reset()
          }}
        />
      )}
      <div className="overflow-x-auto rounded-lg border">
        <Table aria-label={t('policy.title')}>
          <TableHeader>
            <TableRow>
              <TableHead>{t('policy.columns.key')}</TableHead>
              <TableHead>{t('policy.columns.effective')}</TableHead>
              <TableHead>{t('policy.columns.source')}</TableHead>
              <TableHead>{t('policy.columns.override')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {schema.map((k) => {
              const entry = byKey.get(k.key)
              const editable = !k.locked && (level === 'org' || k.project_overridable)
              const id = `pol-${slug(k.key)}`
              return (
                <TableRow key={k.key} data-key={k.key}>
                  <TableCell className="whitespace-normal">
                    <div className="font-medium">{t(`policy.keys.${slug(k.key)}.label`, { defaultValue: k.key })}</div>
                    <div className="text-xs text-muted-foreground">{t(`policy.keys.${slug(k.key)}.help`, { defaultValue: '' })}</div>
                    <Ltr className="text-xs text-muted-foreground">{k.key}</Ltr>
                  </TableCell>
                  <TableCell>
                    <ValueText value={entry?.value ?? k.default} />
                  </TableCell>
                  <TableCell>
                    {entry && (
                      <Badge variant="outline" data-source={entry.source.layer}>
                        {t(`policy.source.${entry.source.layer}`)}
                        {entry.source.layer !== 'product' && <span className="tabular-nums"> v{entry.source.number}</span>}
                      </Badge>
                    )}
                  </TableCell>
                  <TableCell>
                    {k.locked ? (
                      <span className="inline-flex items-center gap-1 text-xs text-muted-foreground">
                        <LockIcon className="size-3.5" aria-hidden />
                        {t('policy.locked')}
                      </span>
                    ) : !editable ? (
                      <span className="text-xs text-muted-foreground">{t('policy.orgOnly')}</span>
                    ) : (
                      <div className="flex items-center gap-2">
                        <ValueControl
                          key={`${k.key}-${k.key in overrides}`}
                          id={id}
                          label={t(`policy.keys.${slug(k.key)}.label`, { defaultValue: k.key })}
                          def={k}
                          value={k.key in overrides ? overrides[k.key] : (entry?.value ?? k.default)}
                          overridden={k.key in overrides}
                          error={fieldErrors[k.key] ?? fieldErrors[`overrides.${k.key}`]}
                          onChange={(v) => set(k.key, v)}
                        />
                        {k.key in overrides && (
                          <IconButton type="button" size="icon-sm" variant="ghost" aria-label={t('policy.resetKey', { key: k.key })} onClick={() => reset(k.key)}>
                            <RotateCcwIcon />
                          </IconButton>
                        )}
                      </div>
                    )}
                  </TableCell>
                </TableRow>
              )
            })}
          </TableBody>
        </Table>
      </div>
      {generalError && (
        <Alert variant="destructive" role="alert">
          <AlertDescription>{generalError}</AlertDescription>
        </Alert>
      )}
      <div className="flex flex-wrap gap-2">
        <Button type="button" variant="outline" disabled={!dirty || save.isPending} onClick={() => runSave(overrides, false)}>
          {t('policy.saveDraft')}
        </Button>
        <Button type="button" disabled={save.isPending || publish.isPending || (!dirty && !base.differs_from_published && base.revision !== 0)} onClick={() => (dirty ? runSave(overrides, true) : runPublish())}>
          {t('policy.publish')}
        </Button>
        {level === 'org' && base.revision === 0 && versionNumbers.length === 0 && (
          <Button type="button" variant="secondary" disabled={save.isPending || publish.isPending} onClick={() => runSave({}, true)}>
            {t('policy.useDefaults')}
          </Button>
        )}
        {dirty && (
          <Button type="button" variant="ghost" onClick={() => setOverrides(base.overrides as Overrides)}>
            {t('policy.discard')}
          </Button>
        )}
      </div>
      <section aria-labelledby="pol-versions" className="flex flex-col gap-2">
        <h3 id="pol-versions" className="text-sm font-medium">
          {t('policy.versions')}
        </h3>
        {versionNumbers.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t('policy.noVersions')}</p>
        ) : (
          <ul className="flex flex-col gap-1 text-sm">
            {versionNumbers.map((v) => (
              <li key={v.id} className="flex items-center gap-3">
                <Badge variant="secondary">v{v.number}</Badge>
                <time dateTime={v.created_at} className="text-xs text-muted-foreground">
                  {formatDateTime(v.created_at, locale)}
                </time>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  )
}

function ValueText({ value }: { value: unknown }) {
  const { t } = useTranslation()
  if (typeof value === 'boolean') return <span>{value ? t('policy.on') : t('policy.off')}</span>
  if (typeof value === 'string') return <Ltr className="font-sans">{t(`policy.values.${value}`, { defaultValue: value })}</Ltr>
  return <Ltr className="font-sans tabular-nums">{String(value ?? '')}</Ltr>
}

function ValueControl({ id, label, def, value, overridden, error, onChange }: { id: string; label: string; def: PolicyKeyDto; value: unknown; overridden: boolean; error?: string; onChange: (v: unknown) => void }) {
  const { t } = useTranslation()
  const common = { id, 'aria-label': label, 'aria-invalid': error ? true : undefined }
  const [text, setText] = useState(String(value ?? ''))
  switch (def.type) {
    case 'bool':
      return <Switch {...common} checked={value === true} onCheckedChange={onChange} data-overridden={overridden} />
    case 'enum':
      return (
        <Select value={String(value)} onValueChange={onChange}>
          <SelectTrigger {...common} className="w-40">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {(def.enum ?? []).map((v) => (
              <SelectItem key={v} value={v}>
                {t(`policy.values.${v}`, { defaultValue: v })}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      )
    case 'int':
      return (
        <Input
          {...common}
          type="number"
          inputMode="numeric"
          dir="ltr"
          className="w-28"
          min={def.min}
          max={def.max}
          value={text}
          onChange={(e) => {
            setText(e.target.value)
            const n = Number(e.target.value)
            if (e.target.value.trim() !== '' && Number.isInteger(n)) onChange(n)
          }}
        />
      )
    default:
      return <Input {...common} dir="ltr" className="w-48" value={String(value ?? '')} onChange={(e) => onChange(e.target.value)} />
  }
}
