import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { ContextManifestDto, RunDto } from '@/api/generated/model'
import { useArtifactText } from '@/api/hooks/projects'
import { useRoleContext, useRunContext } from '@/api/hooks/tasks'
import { ApiProblem } from '@/api/problem'
import { Ltr } from '@/components/ltr'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { formatDateTime, formatNumber } from '@/lib/format'
import { useLocale } from '@/lib/use-locale'

export function ContextTab({ orgId, run }: { orgId: string; run: RunDto | undefined }) {
  const { t } = useTranslation()
  const built = !!run?.context
  const ctx = useRunContext(orgId, run?.id, built)
  if (!run) return <p className="text-sm text-muted-foreground">{t('taskDetail.noRun')}</p>
  if (!built) return <p className="text-sm text-muted-foreground">{t('context.notBuilt')}</p>
  if (ctx.isPending) return <LoadingBlock />
  if (ctx.isError) {
    if (ctx.error instanceof ApiProblem && ctx.error.status === 404) return <p className="text-sm text-muted-foreground">{t('context.notBuilt')}</p>
    return <LoadFailed title={t('context.loadFailed')} error={ctx.error} onRetry={() => void ctx.refetch()} />
  }
  return (
    <div className="flex flex-col gap-4">
      <Manifest orgId={orgId} manifest={ctx.data} />
      <RolePackets orgId={orgId} runId={run.id} />
    </div>
  )
}

const ROLES = ['architect', 'worker', 'verifier', 'reviewer', 'writer'] as const

/** Per-role packet manifests (WP82): knowledge items with trust and freshness, and consumption when the run is over. */
function RolePackets({ orgId, runId }: { orgId: string; runId: string }) {
  const { t } = useTranslation()
  const [role, setRole] = useState<string | undefined>()
  const rc = useRoleContext(orgId, runId, role ?? '', !!role)
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('context.rolePackets')}</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-3 text-sm">
        <div className="flex flex-wrap gap-2">
          {ROLES.map((r) => (
            <Button key={r} size="sm" variant={role === r ? 'secondary' : 'outline'} aria-pressed={role === r} onClick={() => setRole(r)}>
              {t(`context.role.${r}`)}
            </Button>
          ))}
        </div>
        {!role && <p className="text-muted-foreground">{t('context.pickRole')}</p>}
        {role && rc.isPending && <LoadingBlock rows={2} />}
        {rc.isError && (rc.error instanceof ApiProblem && rc.error.status === 404 ? <p className="text-muted-foreground">{t('context.roleNotBuilt')}</p> : <LoadFailed title={t('context.loadFailed')} error={rc.error} onRetry={() => void rc.refetch()} />)}
        {rc.data && <RoleManifest manifest={rc.data.manifest as unknown as RoleManifestShape} oversize={!!rc.data.oversize} />}
      </CardContent>
    </Card>
  )
}

interface RoleItem {
  source_id: string
  kind: string
  label: string
  bytes: number
  included: boolean
  truncated: boolean
  reason?: string
  trust?: string
  freshness?: string
}
interface RoleManifestShape {
  sections: { id: string; bytes: number; items: RoleItem[] }[]
  excluded: { source_id: string; label: string; reason: string }[]
  consumption?: { packet_bytes: number; cap: number; critical_bytes: number; est_tokens: number; est_method: string; actual_input_tokens: number | null; host_overhead: string }
}

function RoleManifest({ manifest, oversize }: { manifest: RoleManifestShape; oversize: boolean }) {
  const { t } = useTranslation()
  const locale = useLocale()
  const [json, setJson] = useState(false)
  const c = manifest.consumption
  const n = (v: number) => formatNumber(v, locale)
  return (
    <div className="flex flex-col gap-3">
      {oversize && <Badge variant="destructive">{t('context.packetOversize')}</Badge>}
      {c && (
        <table className="w-full text-sm" data-testid="consumption">
          <caption className="pb-1 text-start font-medium">{t('context.consumption.title')}</caption>
          <tbody>
            <tr><th scope="row" className="text-start font-normal text-muted-foreground">{t('context.consumption.packetBytes')}</th><td className="tabular-nums">{n(c.packet_bytes)} / {n(c.cap)}</td></tr>
            <tr><th scope="row" className="text-start font-normal text-muted-foreground">{t('context.critical')}</th><td className="tabular-nums">{n(c.critical_bytes)}</td></tr>
            <tr><th scope="row" className="text-start font-normal text-muted-foreground">{t('context.consumption.estTokens')}</th><td className="tabular-nums">{n(c.est_tokens)} <span className="text-xs text-muted-foreground">({c.est_method})</span></td></tr>
            <tr><th scope="row" className="text-start font-normal text-muted-foreground">{t('context.consumption.actual')}</th><td className="tabular-nums">{c.actual_input_tokens == null ? t('context.consumption.notReported') : n(c.actual_input_tokens)}</td></tr>
            <tr><th scope="row" className="text-start font-normal text-muted-foreground">{t('context.consumption.overhead')}</th><td>{c.host_overhead || '—'}</td></tr>
          </tbody>
        </table>
      )}
      {manifest.sections.map((sec) => (
        <div key={sec.id}>
          <h4 className="font-medium">{t(`context.sections.${sec.id}`, { defaultValue: sec.id })} <span className="text-xs tabular-nums text-muted-foreground">{n(sec.bytes)}</span></h4>
          {sec.items.length === 0 ? (
            <p className="text-muted-foreground">{t('context.sectionEmpty')}</p>
          ) : (
            <ul className="flex flex-col gap-1">
              {sec.items.map((it) => (
                <li key={`${sec.id}:${it.source_id}`} className="flex flex-wrap items-center gap-2">
                  <Ltr>{it.label}</Ltr>
                  <Badge variant={it.included ? 'secondary' : 'outline'}>{it.included ? t('context.included') : t(`context.excluded.${it.reason}`, { defaultValue: it.reason ?? '' })}</Badge>
                  {it.trust && <Badge variant="outline">{t(`context.trust.${it.trust}`, { defaultValue: it.trust })}</Badge>}
                  {it.freshness && <Badge variant="outline">{t(`context.freshnessState.${it.freshness}`, { defaultValue: it.freshness })}</Badge>}
                  {it.truncated && <Badge variant="outline">{t('context.truncated')}</Badge>}
                  <span className="text-xs tabular-nums text-muted-foreground">{n(it.bytes)}</span>
                </li>
              ))}
            </ul>
          )}
        </div>
      ))}
      {manifest.excluded.length > 0 && (
        <div>
          <h4 className="font-medium">{t('context.excluded.title', { defaultValue: '' })}</h4>
          <ul className="flex flex-col gap-1">
            {manifest.excluded.map((e) => (
              <li key={e.source_id}><Ltr>{e.label}</Ltr> <Badge variant="outline">{t(`context.excluded.${e.reason}`, { defaultValue: e.reason })}</Badge></li>
            ))}
          </ul>
        </div>
      )}
      <div>
        <Button size="sm" variant="outline" aria-expanded={json} onClick={() => setJson((v) => !v)}>{json ? t('context.hideJson') : t('context.showJson')}</Button>
      </div>
      {json && (
        <pre dir="ltr" className="max-h-96 overflow-auto whitespace-pre-wrap rounded-md border bg-muted p-3 font-mono text-xs">{JSON.stringify(manifest, null, 2)}</pre>
      )}
    </div>
  )
}

function Manifest({ orgId, manifest }: { orgId: string; manifest: ContextManifestDto }) {
  const { t } = useTranslation()
  const locale = useLocale()
  const [open, setOpen] = useState(false)
  const packet = useArtifactText(orgId, open ? manifest.packet_artifact_id : undefined)
  const pct = Math.min(100, Math.round((manifest.bytes / Math.max(manifest.cap, 1)) * 100))
  const criticalPct = Math.min(100, Math.round((manifest.critical_bytes / Math.max(manifest.cap, 1)) * 100))
  return (
    <div className="flex flex-col gap-4">
      <Card>
        <CardHeader>
          <CardTitle>{t('context.title')}</CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col gap-3 text-sm">
          <div
            role="meter"
            aria-label={t('context.meter')}
            aria-valuemin={0}
            aria-valuemax={manifest.cap}
            aria-valuenow={manifest.bytes}
            aria-valuetext={t('context.meterText', { bytes: formatNumber(manifest.bytes, locale), cap: formatNumber(manifest.cap, locale) })}
            className="relative h-3 w-full overflow-hidden rounded-full bg-muted"
          >
            <div className="absolute inset-y-0 start-0 bg-primary/70" style={{ width: `${pct}%` }} />
            <div className="absolute inset-y-0 start-0 bg-primary" style={{ width: `${criticalPct}%` }} />
          </div>
          <p>
            {t('context.bytes')}: <span className="tabular-nums">{formatNumber(manifest.bytes, locale)}</span> / <span className="tabular-nums">{formatNumber(manifest.cap, locale)}</span> · {t('context.critical')}:{' '}
            <span className="tabular-nums">{formatNumber(manifest.critical_bytes, locale)}</span>
          </p>
          <p className="text-xs text-muted-foreground">
            {t('context.freshness', { revision: manifest.freshness.issue_snapshot_revision })} · <Ltr>{manifest.freshness.repo_head_sha.slice(0, 12) || '—'}</Ltr> · {formatDateTime(manifest.built_at, locale)}
          </p>
          <div>
            <Button size="sm" variant="outline" aria-expanded={open} onClick={() => setOpen((v) => !v)}>
              {open ? t('context.hidePacket') : t('context.openPacket')}
            </Button>
          </div>
          {open && (
            <>
              {packet.isPending && <LoadingBlock rows={2} />}
              {packet.isError && <LoadFailed title={t('context.loadFailed')} error={packet.error} onRetry={() => void packet.refetch()} />}
              {packet.data !== undefined && !packet.isPending && (
                <pre dir="auto" className="max-h-96 overflow-auto whitespace-pre-wrap rounded-md border bg-muted p-3 font-mono text-xs">
                  {String(packet.data)}
                </pre>
              )}
            </>
          )}
        </CardContent>
      </Card>
      {manifest.sections.map((s) => (
        <Card key={s.id}>
          <CardHeader>
            <CardTitle>
              {t(`context.sections.${s.id}`, { defaultValue: s.id })} <span className="text-xs font-normal text-muted-foreground tabular-nums">({formatNumber(s.bytes, locale)} B)</span>
            </CardTitle>
          </CardHeader>
          <CardContent>
            {s.items.length === 0 ? (
              <p className="text-sm text-muted-foreground">{t('context.sectionEmpty')}</p>
            ) : (
              <ul className="flex flex-col gap-2 text-sm">
                {s.items.map((it) => (
                  <li key={it.source_id} className="flex flex-wrap items-baseline gap-2">
                    <span dir="auto" className="font-medium">
                      {it.label}
                    </span>
                    <Ltr className="text-xs text-muted-foreground">{it.kind}</Ltr>
                    {it.included ? (
                      <Badge variant="outline">{t('context.included')}</Badge>
                    ) : (
                      <Badge variant="destructive">{t(`context.excluded.${it.reason ?? 'budget'}`, { defaultValue: it.reason ?? '' })}</Badge>
                    )}
                    {it.truncated && <Badge variant="secondary">{t('context.truncated', { original: formatNumber(it.original_bytes, locale) })}</Badge>}
                    {it.included && <span className="text-xs text-muted-foreground tabular-nums">{formatNumber(it.bytes, locale)} B</span>}
                  </li>
                ))}
              </ul>
            )}
          </CardContent>
        </Card>
      ))}
    </div>
  )
}
