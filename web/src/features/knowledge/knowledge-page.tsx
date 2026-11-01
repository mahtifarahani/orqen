import { useState } from 'react'
import type { TFunction } from 'i18next'
import { useTranslation } from 'react-i18next'
import { useParams } from 'react-router'
import { toast } from 'sonner'
import type { KnowledgeConflict, KnowledgeRecord, KnowledgeVersion } from '@/api/generated/model'
import {
  useArchiveRecord,
  useConflicts,
  useKnowledgeList,
  useKnowledgeRecord,
  useKnowledgeSearch,
  useKnowledgeVersionList,
  useRejectVersion,
  useResolveConflict,
  useRollback,
  useVerifyVersion,
  useWikiStatus,
} from '@/api/hooks/knowledge'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { problemMessage } from '@/lib/problem-message'
import { AuthorSessions } from './author-sessions'
import { ProposalsSection } from './proposals-section'

function degradedReason(t: TFunction, code: string): string {
  return t(`knowledgePage.wiki.code.${code}`, { defaultValue: t('knowledgePage.wiki.code.unknown', { code }) })
}

export function KnowledgePage() {
  const { t } = useTranslation()
  const { orgId = '' } = useParams()
  const [selected, setSelected] = useState<string | undefined>()
  const [q, setQ] = useState('')
  const list = useKnowledgeList(orgId)
  const wiki = useWikiStatus(orgId)
  const search = useKnowledgeSearch(orgId, q)
  const conflicts = useConflicts(orgId)

  return (
    <div className="flex flex-col gap-6">
      <header>
        <h1 className="text-2xl font-semibold">{t('knowledgePage.title')}</h1>
        <p className="text-sm text-muted-foreground">{t('knowledgePage.subtitle')}</p>
        {wiki.data && (
          <p className="mt-2 flex flex-wrap items-center gap-2 text-sm" data-testid="wiki-status">
            <Badge variant={wiki.data.mode === 'wiki' ? 'default' : 'secondary'}>{t(`knowledgePage.wiki.${wiki.data.mode}`)}</Badge>
            {wiki.data.pending > 0 && <span>{t('knowledgePage.wiki.pending', { count: wiki.data.pending })}</span>}
            {wiki.data.degraded && <span>{t('knowledgePage.wiki.degraded', { reason: degradedReason(t, (wiki.data.degraded as { code?: string }).code ?? '') })}</span>}
          </p>
        )}
      </header>

      <section aria-label={t('knowledgePage.search')} className="flex max-w-xl flex-col gap-2">
        <Input dir="auto" value={q} onChange={(e) => setQ(e.target.value)} placeholder={t('knowledgePage.searchPlaceholder')} aria-label={t('knowledgePage.search')} />
        {q.trim() && search.data && (
          <ul className="flex flex-col gap-1 text-sm">
            {search.data.items.length === 0 && <li className="text-muted-foreground">{t('knowledgePage.noHits')}</li>}
            {search.data.items.map((h) => (
              <li key={h.version_id}>
                <button type="button" className="text-start underline-offset-2 hover:underline" onClick={() => setSelected(h.record_id)}>
                  <bdi>{h.title}</bdi>
                </button>
              </li>
            ))}
          </ul>
        )}
      </section>

      {(conflicts.data?.items.length ?? 0) > 0 && (
        <section className="flex flex-col gap-3">
          <h2 className="text-lg font-semibold">{t('knowledgePage.conflicts')}</h2>
          <p className="text-sm text-muted-foreground">{t('knowledgePage.conflictHelp')}</p>
          {conflicts.data?.items.map((c) => <ConflictCard key={c.id} orgId={orgId} conflict={c} />)}
        </section>
      )}

      <ProposalsSection orgId={orgId} onOpen={setSelected} />
      <AuthorSessions orgId={orgId} />

      <div className="grid gap-6 md:grid-cols-[minmax(0,20rem)_minmax(0,1fr)]">
        <section aria-label={t('knowledgePage.records')} className="flex flex-col gap-2">
          <h2 className="text-lg font-semibold">{t('knowledgePage.records')}</h2>
          {list.isPending && <LoadingBlock />}
          {list.isError && <LoadFailed title={t('knowledgePage.title')} error={list.error} onRetry={() => void list.refetch()} />}
          {list.data && list.data.items.length === 0 && <p className="text-sm text-muted-foreground">{t('knowledgePage.none')}</p>}
          {list.data?.items.map((r: KnowledgeRecord) => (
            <button
              key={r.id}
              type="button"
              onClick={() => setSelected(r.id)}
              aria-pressed={selected === r.id}
              className="flex flex-col gap-1 rounded-lg border p-3 text-start outline-none focus-visible:ring-2 focus-visible:ring-ring aria-pressed:bg-accent"
            >
              <span className="font-medium"><bdi>{r.title}</bdi></span>
              <span className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                <Badge variant="outline">{r.type}</Badge>
                <Badge variant="secondary">{t(`knowledgePage.statusValues.${r.status}`)}</Badge>
                {r.pinned && <Badge>{t('knowledgePage.pinned')}</Badge>}
              </span>
            </button>
          ))}
        </section>
        <section aria-live="polite">{selected ? <RecordDetail orgId={orgId} id={selected} /> : null}</section>
      </div>
    </div>
  )
}

function RecordDetail({ orgId, id }: { orgId: string; id: string }) {
  const { t } = useTranslation()
  const rec = useKnowledgeRecord(orgId, id)
  const versions = useKnowledgeVersionList(orgId, id)
  const revision = rec.data?.revision ?? 0
  const verify = useVerifyVersion(orgId, revision)
  const reject = useRejectVersion(orgId, revision)
  const rollback = useRollback(orgId, revision)
  const archive = useArchiveRecord(orgId, revision)
  const [note, setNote] = useState('')
  const [attest, setAttest] = useState<Record<string, string>>({})
  if (rec.isPending) return <LoadingBlock />
  if (rec.isError || !rec.data) return <LoadFailed title={t('knowledgePage.title')} error={rec.error} onRetry={() => void rec.refetch()} />
  const r = rec.data
  const cur = (r.verified_version ?? r.current_version) as KnowledgeVersion | undefined
  const draft = r.current_version && r.current_version.status !== 'verified' ? r.current_version : undefined
  const fail = (e: unknown) => toast.error(problemMessage(t, e))
  return (
    <Card>
      <CardContent className="flex flex-col gap-4 p-4">
        <div>
          <h2 className="text-xl font-semibold"><bdi>{r.title}</bdi></h2>
          <p className="text-xs text-muted-foreground">{r.scope_key} / {r.key}</p>
        </div>
        {[draft, cur].filter((v, i, a): v is KnowledgeVersion => !!v && a.indexOf(v) === i).map((v) => (
          <VersionView key={v.id} v={v} />
        ))}
        {draft && (
          <div className="flex flex-col gap-2">
            <label className="text-sm font-medium" htmlFor="kn-note">{t('knowledgePage.actions.note')}</label>
            <Textarea id="kn-note" dir="auto" rows={2} value={note} onChange={(e) => setNote(e.target.value)} />
            {draft.claims.filter((c) => c.verdict !== 'supported').map((c) => (
              <div key={c.id} className="flex flex-col gap-1">
                <label className="text-sm font-medium" htmlFor={`kn-attest-${c.id}`}>{t('knowledgePage.actions.attest', { claim: c.text })}</label>
                <Textarea id={`kn-attest-${c.id}`} dir="auto" rows={2} value={attest[c.id] ?? ''} onChange={(e) => setAttest({ ...attest, [c.id]: e.target.value })} />
              </div>
            ))}
            <div className="flex flex-wrap gap-2">
              <Button disabled={verify.isPending} onClick={() => verify.mutate({ orgId, kvrId: draft.id, data: { note, attestations: draft.claims.filter((c) => (attest[c.id] ?? '').trim() !== '').map((c) => ({ claim_id: c.id, text: attest[c.id].trim() })) } }, { onSuccess: () => toast.success(t('knowledgePage.verified')), onError: fail })}>
                {t('knowledgePage.actions.verify')}
              </Button>
              <Button variant="outline" disabled={reject.isPending} onClick={() => reject.mutate({ orgId, kvrId: draft.id, data: { reason: note } }, { onSuccess: () => toast.success(t('knowledgePage.rejected')), onError: fail })}>
                {t('knowledgePage.actions.reject')}
              </Button>
            </div>
          </div>
        )}
        <div>
          <h3 className="mb-2 font-semibold">{t('knowledgePage.versions')}</h3>
          <ul className="flex flex-col gap-1 text-sm">
            {versions.data?.items.map((v) => (
              <li key={v.id} className="flex flex-wrap items-center gap-2">
                <span>{t('knowledgePage.version', { n: v.number })}</span>
                <Badge variant="secondary">{t(`knowledgePage.statusValues.${v.status}`)}</Badge>
                {v.verified_at && v.id !== r.verified_version_id && (
                  <Button size="sm" variant="outline" onClick={() => rollback.mutate({ orgId, knoId: id, data: { to_version_id: v.id, note: '' } }, { onSuccess: () => toast.success(t('knowledgePage.rolledBack')), onError: fail })}>
                    {t('knowledgePage.actions.rollback')}
                  </Button>
                )}
              </li>
            ))}
          </ul>
        </div>
        {r.active && (
          <div>
            <Button variant="outline" onClick={() => archive.mutate({ orgId, knoId: id, data: { reason: '' } }, { onSuccess: () => toast.success(t('knowledgePage.archived')), onError: fail })}>
              {t('knowledgePage.actions.archive')}
            </Button>
          </div>
        )}
      </CardContent>
    </Card>
  )
}

function VersionView({ v }: { v: KnowledgeVersion }) {
  const { t } = useTranslation()
  return (
    <div className="flex flex-col gap-2 rounded-lg border p-3">
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <Badge variant="secondary">{t(`knowledgePage.statusValues.${v.status}`)}</Badge>
        <span>{t('knowledgePage.version', { n: v.number })}</span>
        {v.evidence_stale && <Badge variant="destructive">{t('knowledgePage.stale')}</Badge>}
        <span className="text-xs text-muted-foreground">{t(`knowledgePage.origin.${(v.origin as { kind?: string }).kind ?? 'user'}`, { defaultValue: '' })}</span>
      </div>
      {v.body && <p className="whitespace-pre-wrap text-sm" dir="auto">{v.body}</p>}
      <h3 className="text-sm font-semibold">{t('knowledgePage.claims')}</h3>
      <ul className="flex flex-col gap-2 text-sm">
        {v.claims.map((c) => (
          <li key={c.id} className="flex flex-col gap-1">
            <span dir="auto">{c.text}</span>
            <span className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
              <Badge variant="outline">{t(`knowledgePage.verdict.${c.verdict}`)}</Badge>
              {c.critical && <Badge>{t('knowledgePage.critical')}</Badge>}
              {c.evidence.length === 0 ? t('knowledgePage.noEvidence') : c.evidence.map((e) => <span key={`${e.ref_kind}${e.ref_id}`}><bdi>{e.label || e.ref_id}</bdi></span>)}
            </span>
          </li>
        ))}
      </ul>
    </div>
  )
}

function ConflictCard({ orgId, conflict }: { orgId: string; conflict: KnowledgeConflict }) {
  const { t } = useTranslation()
  const resolve = useResolveConflict(orgId, conflict.revision)
  const [note, setNote] = useState('')
  const go = (choice: 'keep_existing' | 'accept_new' | 'archive_both') =>
    resolve.mutate({ orgId, kcfId: conflict.id, data: { choice, note, attestations: [] } }, { onError: (e) => toast.error(problemMessage(t, e)) })
  return (
    <Card>
      <CardContent className="flex flex-col gap-2 p-4">
        <Textarea dir="auto" rows={2} value={note} onChange={(e) => setNote(e.target.value)} placeholder={t('knowledgePage.resolve.note')} aria-label={t('knowledgePage.resolve.note')} />
        <div className="flex flex-wrap gap-2">
          <Button variant="outline" onClick={() => go('keep_existing')}>{t('knowledgePage.resolve.keep_existing')}</Button>
          <Button onClick={() => go('accept_new')}>{t('knowledgePage.resolve.accept_new')}</Button>
          <Button variant="outline" onClick={() => go('archive_both')}>{t('knowledgePage.resolve.archive_both')}</Button>
        </div>
      </CardContent>
    </Card>
  )
}
