import { IconButton } from '@/components/icon-button'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronDownIcon, ChevronUpIcon, PlusIcon, XIcon } from 'lucide-react'
import type { CatherdIssue, CatherdProfileDoc, CatherdSchemaDto, ProfileMeta } from '@/api/generated/model'
import { useCatalog } from '@/api/hooks/catherd'
import { Field } from '@/components/field'
import { Ltr } from '@/components/ltr'
import { NumberField } from '@/components/number-field'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { cn } from '@/lib/utils'
import { addRung, allRungs, moveRung, removeRung, roleOf, setFailover, setRole, setSection, setTop, toggleNotify } from './profile-doc'
import { RungPicker } from './rung-picker'

export interface SectionProps {
  orgId: string
  doc: CatherdProfileDoc
  meta: ProfileMeta
  schema: CatherdSchemaDto
  readOnly: boolean
  onDoc: (d: CatherdProfileDoc) => void
  onMeta: (m: ProfileMeta) => void
  /** Role the latest published version resolved, used for the enforcement word. */
  enforcement?: (role: string) => string | undefined
}
const DEFAULT = '__default'
type Obj = Record<string, unknown>
const num = (v: unknown) => (typeof v === 'number' ? v : undefined)

export function RolesSection(p: SectionProps) {
  const { t } = useTranslation()
  const [open, setOpen] = useState<string | null>(null)
  const [picker, setPicker] = useState<string | null>(null)
  return (
    <div className="flex flex-col gap-3">
      <p className="max-w-prose text-sm text-muted-foreground">{t('profile.roles.help')}</p>
      <ul className="flex flex-col gap-3">
        {p.schema.roles.map((role) => {
          const r = roleOf(p.doc, role)
          const rungs = r.rungs ?? []
          const enabled = r.enabled !== false
          const expanded = open === role
          const bodyId = `role-${role}-body`
          return (
            <li key={role} className="rounded-lg border" data-role={role}>
              <div className="flex flex-wrap items-center gap-3 p-3">
                <Switch aria-label={t('profile.roles.enabled', { role: t(`profile.roleNames.${role}`, { defaultValue: role }) })} checked={enabled} disabled={p.readOnly || role === 'worker'} onCheckedChange={(v) => p.onDoc(setRole(p.doc, role, { enabled: v }))} />
                <button type="button" className="flex min-w-0 flex-1 flex-col items-start rounded-md text-start outline-none focus-visible:ring-2 focus-visible:ring-ring" aria-expanded={expanded} aria-controls={bodyId} onClick={() => setOpen(expanded ? null : role)}>
                  <span className="font-medium">{t(`profile.roleNames.${role}`, { defaultValue: role })}</span>
                  <span className="text-xs text-muted-foreground">
                    {rungs.length === 0 ? t('profile.roles.noRungs') : (
                      <>
                        <Ltr className="font-sans">{r.defaultRung ?? rungs[0]}</Ltr>
                        {rungs.length > 1 && <> {t('profile.roles.more', { count: rungs.length - 1 })}</>}
                      </>
                    )}
                  </span>
                </button>
                <Badge variant="outline">{t(`profile.access.modes.${r.access ?? 'read-only'}`)}</Badge>
                <ChevronDownIcon aria-hidden className={cn('size-4 transition-transform', expanded && 'rotate-180')} />
              </div>
              {expanded && (
                <div id={bodyId} className="flex flex-col gap-4 border-t p-3">
                  <div role="radiogroup" aria-label={t('profile.access.label', { role })} className="flex flex-wrap items-center gap-2">
                    {p.schema.access.map((a) => (
                      <Button key={a} type="button" role="radio" aria-checked={(r.access ?? 'read-only') === a} size="sm" variant={(r.access ?? 'read-only') === a ? 'default' : 'outline'} disabled={p.readOnly} onClick={() => p.onDoc(setRole(p.doc, role, { access: a as 'read-only' }))}>
                        {t(`profile.access.modes.${a}`)}
                      </Button>
                    ))}
                    <span className="text-xs text-muted-foreground">
                      {t('profile.access.enforcement')}: {p.enforcement?.(role) ?? t(`profile.access.enforcementDefault.${r.access ?? 'read-only'}`)}
                    </span>
                  </div>

                  <div>
                    <h4 className="mb-1 text-sm font-medium">{t('profile.roles.ladder')}</h4>
                    {rungs.length === 0 && <p className="text-sm text-muted-foreground">{t('profile.roles.noRungs')}</p>}
                    <ol className="flex flex-col gap-1" aria-label={t('profile.roles.ladder')}>
                      {rungs.map((rung, i) => (
                        <li key={rung} className="flex items-center gap-2 rounded-md border px-2 py-1">
                          <span className="w-5 text-xs text-muted-foreground tabular-nums">{i + 1}</span>
                          <Ltr className="flex-1 break-all text-xs">{rung}</Ltr>
                          {r.defaultRung === rung && <Badge variant="secondary">{t('profile.roles.startsOn')}</Badge>}
                          <IconButton type="button" size="icon-sm" variant="ghost" disabled={p.readOnly || i === 0} aria-label={t('profile.roles.moveUp', { rung })} onClick={() => p.onDoc(moveRung(p.doc, role, i, -1))}>
                            <ChevronUpIcon />
                          </IconButton>
                          <IconButton type="button" size="icon-sm" variant="ghost" disabled={p.readOnly || i === rungs.length - 1} aria-label={t('profile.roles.moveDown', { rung })} onClick={() => p.onDoc(moveRung(p.doc, role, i, 1))}>
                            <ChevronDownIcon />
                          </IconButton>
                          <IconButton type="button" size="icon-sm" variant="ghost" disabled={p.readOnly} aria-label={t('profile.roles.removeRung', { rung })} onClick={() => p.onDoc(removeRung(p.doc, role, i))}>
                            <XIcon />
                          </IconButton>
                        </li>
                      ))}
                    </ol>
                    <Button type="button" size="sm" variant="outline" className="mt-2" disabled={p.readOnly} onClick={() => setPicker(role)}>
                      <PlusIcon />
                      {t('profile.roles.addModel')}
                    </Button>
                  </div>

                  <div className="grid gap-4 sm:grid-cols-2">
                    <Field id={`role-${role}-default`} label={t('profile.roles.defaultRung')}>
                      {(f) => (
                        <Select value={r.defaultRung ?? DEFAULT} onValueChange={(v) => p.onDoc(setRole(p.doc, role, { defaultRung: v === DEFAULT ? null : v }))} disabled={p.readOnly || rungs.length === 0}>
                          <SelectTrigger {...f} className="w-full">
                            <SelectValue />
                          </SelectTrigger>
                          <SelectContent>
                            <SelectItem value={DEFAULT}>{t('profile.roles.firstRung')}</SelectItem>
                            {rungs.map((x) => (
                              <SelectItem key={x} value={x}>
                                {x}
                              </SelectItem>
                            ))}
                          </SelectContent>
                        </Select>
                      )}
                    </Field>
                    <Field id={`role-${role}-network`} label={t('profile.roles.network')}>
                      {(f) => (
                        <Select value={r.network == null ? DEFAULT : r.network ? 'on' : 'off'} onValueChange={(v) => p.onDoc(setRole(p.doc, role, { network: v === DEFAULT ? null : v === 'on' }))} disabled={p.readOnly}>
                          <SelectTrigger {...f} className="w-full">
                            <SelectValue />
                          </SelectTrigger>
                          <SelectContent>
                            <SelectItem value={DEFAULT}>{t('profile.roles.networkDefault')}</SelectItem>
                            <SelectItem value="on">{t('policy.on')}</SelectItem>
                            <SelectItem value="off">{t('policy.off')}</SelectItem>
                          </SelectContent>
                        </Select>
                      )}
                    </Field>
                    {(['idleMin', 'wallMin'] as const).map((k) => (
                      <Field key={k} id={`role-${role}-${k}`} label={t(`profile.lifecycle.${k}`)} hint={t('profile.roles.timeoutHint')}>
                        {(f) => (
                          <NumberField
                            {...f}
                            disabled={p.readOnly}
                            value={num((r.timeouts as Obj | null | undefined)?.[k])}
                            onCommit={(n) => {
                              const cur = { ...((r.timeouts as Obj | null | undefined) ?? {}) }
                              if (n === undefined) delete cur[k]
                              else cur[k] = n
                              p.onDoc(setRole(p.doc, role, { timeouts: Object.keys(cur).length ? cur : null }))
                            }}
                          />
                        )}
                      </Field>
                    ))}
                  </div>
                </div>
              )}
            </li>
          )
        })}
      </ul>
      {picker && <RungPicker orgId={p.orgId} role={picker} existing={roleOf(p.doc, picker).rungs ?? []} onAdd={(rung) => p.onDoc(addRung(p.doc, picker, rung))} onClose={() => setPicker(null)} />}
    </div>
  )
}

function Segmented({ value, options, label, disabled, onChange }: { value: string; options: { value: string; label: string }[]; label: string; disabled?: boolean; onChange: (v: string) => void }) {
  return (
    <div role="radiogroup" aria-label={label} className="flex flex-wrap gap-2">
      {options.map((o) => (
        <Button key={o.value} type="button" role="radio" aria-checked={value === o.value} size="sm" variant={value === o.value ? 'default' : 'outline'} disabled={disabled} onClick={() => onChange(o.value)}>
          {o.label}
        </Button>
      ))}
    </div>
  )
}

export function RoutingSection(p: SectionProps) {
  const { t } = useTranslation()
  const catalog = useCatalog(p.orgId, {})
  const rungs = allRungs(p.doc, p.schema.roles)
  const failover = (p.doc.failover ?? {}) as Record<string, string>
  const jevUse = ((p.doc.jev as Obj | undefined)?.use as string | undefined) ?? 'auto'
  const known = catalog.data?.models.flatMap((m) => m.rungs.filter((r) => r.availability === 'available').map((r) => r.rung)) ?? []
  return (
    <div className="flex flex-col gap-6">
      <section className="flex flex-col gap-2">
        <h3 className="text-sm font-medium">{t('profile.routing.objective')}</h3>
        <Segmented label={t('profile.routing.objective')} disabled={p.readOnly} value={p.doc.objective ?? 'cost'} options={p.schema.objectives.map((o) => ({ value: o, label: t(`profile.routing.objectives.${o}`) }))} onChange={(v) => p.onDoc(setTop(p.doc, 'objective', v))} />
        <p className="text-xs text-muted-foreground">{t('profile.routing.objectiveHelp')}</p>
      </section>
      <section className="flex items-center gap-3">
        <Switch id="jev" checked={jevUse === 'auto'} disabled={p.readOnly} onCheckedChange={(v) => p.onDoc(setSection(p.doc, 'jev', 'use', v ? 'auto' : 'off'))} />
        <Label htmlFor="jev">{t('profile.routing.jev')}</Label>
      </section>
      <section className="flex flex-col gap-2">
        <h3 className="text-sm font-medium">{t('profile.routing.failover')}</h3>
        <p className="max-w-prose text-xs text-muted-foreground">{t('profile.routing.failoverHelp')}</p>
        <datalist id="failover-known">
          {known.map((k) => (
            <option key={k} value={k}>
              {k}
            </option>
          ))}
        </datalist>
        {rungs.length === 0 && <p className="text-sm text-muted-foreground">{t('profile.routing.noRungs')}</p>}
        <ul className="flex flex-col gap-2">
          {rungs.map((rung, i) => (
            <li key={rung} className="grid items-center gap-2 sm:grid-cols-[1fr_auto_1fr]">
              <Ltr className="break-all text-xs">{rung}</Ltr>
              <span aria-hidden className="hidden text-muted-foreground sm:inline rtl:rotate-180">→</span>
              <Input aria-label={t('profile.routing.standIn', { rung })} dir="ltr" list="failover-known" className="font-mono text-xs" disabled={p.readOnly} value={failover[rung] ?? ''} placeholder={t('profile.routing.noStandIn')} onChange={(e) => p.onDoc(setFailover(p.doc, rung, e.target.value.trim()))} id={`failover-${i}`} />
            </li>
          ))}
        </ul>
      </section>
    </div>
  )
}

export function AccessSection(p: SectionProps) {
  const { t } = useTranslation()
  const harness = (p.doc.harness ?? {}) as Record<string, Obj>
  return (
    <div className="flex flex-col gap-6">
      <section className="flex flex-col gap-2">
        <h3 className="text-sm font-medium">{t('profile.access.isolation')}</h3>
        <p className="max-w-prose text-xs text-muted-foreground">{t('profile.access.isolationHelp')}</p>
        <ul className="flex flex-col gap-2">
          {p.schema.harness_keys.map((k) => (
            <li key={k} className="flex items-center gap-3">
              <Switch id={`iso-${k}`} checked={harness[k]?.isolated === true} disabled={p.readOnly} onCheckedChange={(v) => p.onDoc({ ...setSection(p.doc, 'harness', k, { isolated: v }) })} />
              <Label htmlFor={`iso-${k}`}>
                <Ltr className="font-sans">{k}</Ltr>
              </Label>
            </li>
          ))}
        </ul>
      </section>
      <section className="flex flex-col gap-2">
        <h3 className="text-sm font-medium">{t('profile.access.explainer')}</h3>
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-sm">
          {p.schema.access.map((a) => (
            <div key={a} className="col-span-2 grid grid-cols-subgrid">
              <dt className="font-medium">{t(`profile.access.modes.${a}`)}</dt>
              <dd className="text-muted-foreground">{t(`profile.access.enforcementDefault.${a}`)}</dd>
            </div>
          ))}
        </dl>
      </section>
    </div>
  )
}

export function BillingSection(p: SectionProps) {
  const { t } = useTranslation()
  const billing = (p.doc.billing ?? {}) as Record<string, string>
  const budget = (p.doc.budget ?? {}) as Obj
  const capped = ['minutes', 'tokens', 'usd'].some((k) => budget[k] != null)
  return (
    <div className="flex flex-col gap-6">
      <section className="flex flex-col gap-3">
        <h3 className="text-sm font-medium">{t('profile.billing.modes')}</h3>
        <div className="grid gap-3 sm:grid-cols-2">
          {p.schema.billing_keys.map((k) => (
            <Field key={k} id={`bill-${k}`} label={k}>
              {(f) => (
                <Select value={billing[k] ?? DEFAULT} onValueChange={(v) => p.onDoc(setSection(p.doc, 'billing', k, v === DEFAULT ? undefined : v))} disabled={p.readOnly}>
                  <SelectTrigger {...f} className="w-full" dir="ltr">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value={DEFAULT}>{t('profile.billing.default')}</SelectItem>
                    {p.schema.billing_modes.map((m) => (
                      <SelectItem key={m} value={m}>
                        {m}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
            </Field>
          ))}
        </div>
      </section>
      <section className="flex flex-col gap-3">
        <h3 className="text-sm font-medium">{t('profile.billing.budget')}</h3>
        <div className="grid gap-3 sm:grid-cols-3">
          {(['minutes', 'tokens', 'usd'] as const).map((k) => (
            <Field key={k} id={`budget-${k}`} label={t(`profile.billing.${k}`)}>
              {(f) => <NumberField {...f} disabled={p.readOnly} value={num(budget[k])} onCommit={(n) => p.onDoc(setSection(p.doc, 'budget', k, n))} />}
            </Field>
          ))}
        </div>
        <div className="flex items-start gap-2">
          <Checkbox id="budget-ack" checked={p.meta.budget_uncapped_ack} disabled={p.readOnly || capped} onCheckedChange={(v) => p.onMeta({ ...p.meta, budget_uncapped_ack: v === true })} />
          <Label htmlFor="budget-ack" className="leading-snug">
            {t('profile.billing.noCap')}
          </Label>
        </div>
        {!capped && !p.meta.budget_uncapped_ack && <Alert><AlertDescription>{t('profile.billing.chooseBudget')}</AlertDescription></Alert>}
      </section>
    </div>
  )
}

export function LifecycleSection(p: SectionProps) {
  const { t } = useTranslation()
  const timeouts = (p.doc.timeouts ?? {}) as Obj
  const preflight = (p.doc.preflight ?? {}) as Obj
  const lock = (p.doc.lock ?? {}) as Obj
  const notify = new Set(p.doc.notify ?? [])
  return (
    <div className="flex flex-col gap-6">
      <section className="grid gap-3 sm:grid-cols-2">
        {(['idleMin', 'wallMin'] as const).map((k) => (
          <Field key={k} id={`life-${k}`} label={t(`profile.lifecycle.${k}`)}>
            {(f) => <NumberField {...f} disabled={p.readOnly} value={num(timeouts[k])} onCommit={(n) => p.onDoc(setSection(p.doc, 'timeouts', k, n))} />}
          </Field>
        ))}
      </section>
      <section className="flex items-center gap-3">
        <Switch id="life-confirm" checked={preflight.confirm === true} disabled={p.readOnly} onCheckedChange={(v) => p.onDoc(setSection(p.doc, 'preflight', 'confirm', v))} />
        <Label htmlFor="life-confirm">{t('profile.lifecycle.preflightConfirm')}</Label>
      </section>
      <Field id="life-lock" label={t('profile.lifecycle.lockHeavy')} hint={t('profile.lifecycle.lockHint')}>
        {(f) => (
          <Input
            {...f}
            dir="ltr"
            className="w-40"
            disabled={p.readOnly}
            value={String(lock.heavy ?? '')}
            onChange={(e) => {
              const v = e.target.value.trim()
              p.onDoc(setSection(p.doc, 'lock', 'heavy', v === '' ? undefined : /^\d+$/.test(v) ? Number(v) : v))
            }}
          />
        )}
      </Field>
      <fieldset className="flex flex-col gap-2">
        <legend className="mb-1 text-sm font-medium">{t('profile.lifecycle.notify')}</legend>
        {p.schema.notify.map((n) => (
          <div key={n} className="flex items-center gap-2">
            <Checkbox id={`notify-${n}`} checked={notify.has(n)} disabled={p.readOnly} onCheckedChange={(v) => p.onDoc(toggleNotify(p.doc, n, v === true))} />
            <Label htmlFor={`notify-${n}`}>{t(`profile.lifecycle.notifyItems.${n}`, { defaultValue: n })}</Label>
          </div>
        ))}
      </fieldset>
    </div>
  )
}

export function AdvancedSection({ doc, readOnly, issues, onDoc, onJsonError }: { doc: CatherdProfileDoc; readOnly: boolean; issues: CatherdIssue[]; onDoc: (d: CatherdProfileDoc) => void; onJsonError: (message: string | null) => void }) {
  const { t } = useTranslation()
  const [text, setText] = useState(() => JSON.stringify(doc, null, 2))
  const [error, setError] = useState<string | null>(null)
  const unsupported = issues.filter((i) => i.code === 'unsupported_field')
  const change = (raw: string) => {
    setText(raw)
    try {
      const v: unknown = JSON.parse(raw)
      if (v === null || typeof v !== 'object' || Array.isArray(v)) throw new Error(t('profile.advanced.notObject'))
      setError(null)
      onJsonError(null)
      onDoc(v as CatherdProfileDoc)
    } catch (e) {
      const m = e instanceof Error ? e.message : t('profile.advanced.invalid')
      setError(m)
      onJsonError(m)
    }
  }
  const removeUnsupported = () => {
    const next = structuredClone(doc) as Obj
    for (const i of unsupported) {
      let cur: Obj | undefined = next
      for (const seg of i.path.slice(0, -1)) cur = cur?.[seg] as Obj | undefined
      if (cur) delete cur[i.path[i.path.length - 1]]
    }
    onDoc(next as CatherdProfileDoc)
    setText(JSON.stringify(next, null, 2))
    setError(null)
    onJsonError(null)
  }
  return (
    <div className="flex flex-col gap-3">
      <p className="max-w-prose text-sm text-muted-foreground">{t('profile.advanced.help')}</p>
      <Field id="adv-json" label={t('profile.advanced.json')} error={error ? t('profile.advanced.parseError', { message: error }) : undefined}>
        {(f) => <Textarea {...f} dir="ltr" className="min-h-96 font-mono text-xs" spellCheck={false} readOnly={readOnly} value={text} onChange={(e) => change(e.target.value)} />}
      </Field>
      {unsupported.length > 0 && (
        <Alert>
          <AlertDescription className="flex flex-col items-start gap-2">
            <span>{t('profile.advanced.unsupported')}</span>
            <ul className="list-disc ps-5">
              {unsupported.map((i) => (
                <li key={i.path_display}>
                  <Ltr>{i.path_display}</Ltr>
                </li>
              ))}
            </ul>
            <Button type="button" size="sm" variant="outline" disabled={readOnly} onClick={removeUnsupported}>
              {t('profile.advanced.removeUnsupported')}
            </Button>
          </AlertDescription>
        </Alert>
      )}
    </div>
  )
}
