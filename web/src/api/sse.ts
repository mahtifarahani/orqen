import { useEffect, useState, useSyncExternalStore } from 'react'
import { useQueryClient, type QueryClient } from '@tanstack/react-query'
import { invalidatePath } from './query-keys'
import type { EventDto } from './generated/model'

export type { EventDto }
export type StreamStatus = 'connecting' | 'live' | 'reconnecting'
export interface FeedSnapshot {
  status: StreamStatus
  events: EventDto[]
}

const RING = 200
const RECONNECT_MS = 3000
const BATCH_MS = 150
const REPLAY_SETTLE_MS = 300
export const lastSeqKey = (orgId: string) => `orqen.lastSeq.${orgId}`

function readSeq(orgId: string): number {
  try {
    const n = Number(sessionStorage.getItem(lastSeqKey(orgId)))
    return Number.isFinite(n) && n > 0 ? n : 0
  } catch {
    return 0
  }
}

function writeSeq(orgId: string, seq: number) {
  try {
    sessionStorage.setItem(lastSeqKey(orgId), String(seq))
  } catch {
    /* storage unavailable: resume from 0 */
  }
}

export class EventStore {
  private snap: FeedSnapshot = { status: 'connecting', events: [] }
  private listeners = new Set<() => void>()

  subscribe = (l: () => void) => {
    this.listeners.add(l)
    return () => {
      this.listeners.delete(l)
    }
  }
  getSnapshot = () => this.snap

  private emit(next: FeedSnapshot) {
    this.snap = next
    this.listeners.forEach((l) => l())
  }
  reset() {
    this.emit({ status: 'connecting', events: [] })
  }
  setStatus(status: StreamStatus) {
    if (status !== this.snap.status) this.emit({ ...this.snap, status })
  }
  push(ev: EventDto) {
    if (this.snap.events.some((e) => e.seq === ev.seq)) return
    this.emit({ ...this.snap, events: [...this.snap.events, ev].slice(-RING) })
  }
}

export function invalidateFor(qc: QueryClient, orgId: string, ev: EventDto) {
  const t = ev.type
  const org = `/api/v1/orgs/${orgId}`
  if (t.startsWith('org.')) invalidatePath(qc, (p) => p === '/api/v1/orgs' || p === org)
  if (t.startsWith('connection.')) invalidatePath(qc, (p) => p.startsWith(`${org}/connections`) && !p.includes('/catalog-projects'))
  if (t.startsWith('project.')) invalidatePath(qc, (p) => p.startsWith(`${org}/projects`) || p.startsWith(`${org}/connections`) && p.includes('/catalog-projects'))
  // Progress events carry no list changes; only the finished refresh reloads the catalog pages.
  if (t === 'catalog.refreshed') invalidatePath(qc, (p) => p.startsWith(`${org}/catalog`) || p.startsWith(`${org}/connections`) && p.includes('/catalog-projects'))
  // The orchestrator host is a policy key: a publish changes the host list, the doctor shown and every readiness.
  if (t.startsWith('policy.')) invalidatePath(qc, (p) => p.startsWith(`${org}/policy`) || p.startsWith(`${org}/projects`) || p.startsWith(`${org}/catherd`))
  if (t.startsWith('model_profile')) invalidatePath(qc, (p) => p.startsWith(`${org}/model-profile`) || p.startsWith(`${org}/projects`))
  if (t.startsWith('catherd.') || t.startsWith('catherd_secret.')) invalidatePath(qc, (p) => p.startsWith(`${org}/catherd`) || p === `${org}/health` || p.startsWith(`${org}/projects`) || p.startsWith(`${org}/system`))
  if (t.startsWith('system.')) invalidatePath(qc, (p) => p.startsWith(`${org}/system`) || p === `${org}/health`)
  if (t.startsWith('setup.')) invalidatePath(qc, (p) => p.startsWith(`${org}/setup`))
  // The setup wizard re-checks its criteria on every read, so anything those criteria look at refreshes it too.
  if (/^(connection\.|project\.|policy\.|model_profile|system\.|catalog\.|catherd_secret\.)/.test(t)) invalidatePath(qc, (p) => p.startsWith(`${org}/setup`))
  // Sprint 3: runs, issues, intake, inbox, workspaces, rules and the pause switch.
  if (/^(run\.|issue\.|intake\.)/.test(t)) invalidatePath(qc, (p) => p.startsWith(`${org}/tasks`) || p.startsWith(`${org}/runs`) || p.startsWith(`${org}/projects`) && p.endsWith('/intake') || p === `${org}/inbox`)
  if (t.startsWith('inbox.')) invalidatePath(qc, (p) => p.startsWith(`${org}/inbox`) || p.startsWith(`${org}/tasks`) || p.startsWith(`${org}/runs`))
  if (/^(workspace\.|context\.|decision\.)/.test(t)) invalidatePath(qc, (p) => p.startsWith(`${org}/runs`) || p.startsWith(`${org}/tasks`))
  if (t.startsWith('automation.')) invalidatePath(qc, (p) => p.startsWith(`${org}/automation`) || p.startsWith(`${org}/tasks/summary`) || p.endsWith('/intake'))
  if (t.startsWith('pinned_rule.')) invalidatePath(qc, (p) => p.startsWith(`${org}/rules`))
  // Sprint 4: roles, host, verification, review, merge requests, external operations and write settings.
  if (/^(role\.|host\.|coding_slot\.|verification\.|review\.|finding\.|mr\.|extwrite\.|run\.landed)/.test(t))
    invalidatePath(qc, (p) => p.startsWith(`${org}/runs`) || p.startsWith(`${org}/tasks`) || p.startsWith(`${org}/merge-requests`) || p.startsWith(`${org}/host`) || p === `${org}/inbox`)
  if (t.startsWith('external_writes.') || t.startsWith('write_credential.'))
    invalidatePath(qc, (p) => p.startsWith(`${org}/external-writes`) || p.startsWith(`${org}/connections`) && p.includes('/write-credential') || p.startsWith(`${org}/projects`) && p.endsWith('/readiness'))
  if (t.startsWith('host.')) invalidatePath(qc, (p) => p.startsWith(`${org}/projects`) && p.endsWith('/readiness'))
  // Sprint 6: extensions, skills, security scans, code indexes and backups.
  if (t.startsWith('extension.')) invalidatePath(qc, (p) => p.startsWith(`${org}/extensions`) || p.startsWith(`${org}/projects`) || p.startsWith(`${org}/security`) || p.startsWith(`${org}/graph`) || p === `${org}/health`)
  if (/^skills?\./.test(t)) invalidatePath(qc, (p) => p.startsWith(`${org}/skill`) || p.startsWith(`${org}/runs`))
  if (t.startsWith('security.')) invalidatePath(qc, (p) => p.startsWith(`${org}/security`) || p.startsWith(`${org}/runs`) || p.startsWith(`${org}/tasks`) || p.startsWith(`${org}/merge-requests`) || p.startsWith(`${org}/policy`))
  if (t.startsWith('code_index.') || t.startsWith('codegraph.')) invalidatePath(qc, (p) => p.startsWith(`${org}/code-indexes`) || p.startsWith(`${org}/graph`) || p.startsWith(`${org}/policy`))
  if (/^(backup|restore)\./.test(t)) invalidatePath(qc, (p) => p.startsWith(`${org}/system`) || p === `${org}/health`)
  // Sprint 7: the roles author sessions, their proposals and the records they draft.
  if (t.startsWith('knowledge.')) invalidatePath(qc, (p) => p.startsWith(`${org}/knowledge`) || p.startsWith(`${org}/wiki`))
  if ((t.startsWith('operation.') || t === 'connection_test.stage') && ev.operation_id) {
    const op = `${org}/operations/${ev.operation_id}`
    invalidatePath(qc, (p) => p === op)
  }
}

/**
 * Coalesces a burst of events (the replay after load or a reconnect can carry hundreds) into one invalidation per
 * distinct event kind. invalidateFor reads only the type and the operation id, so nothing else is lost.
 */
export function createInvalidationBatcher(apply: (ev: EventDto) => void, ms = BATCH_MS) {
  const pending = new Map<string, EventDto>()
  let timer: ReturnType<typeof setTimeout> | undefined
  // The first event of a quiet period applies at once; the rest of a burst waits for the window to close.
  const flush = () => {
    const evs = [...pending.values()]
    pending.clear()
    evs.forEach(apply)
    timer = evs.length > 0 ? setTimeout(flush, ms) : undefined
  }
  return {
    add(ev: EventDto) {
      if (!timer) {
        apply(ev)
        timer = setTimeout(flush, ms)
        return
      }
      pending.set(`${ev.type}|${ev.operation_id ?? ''}`, ev)
    },
    cancel() {
      clearTimeout(timer)
      timer = undefined
      pending.clear()
    },
  }
}

/**
 * Opens the org event stream, resumes from the last seen `seq`, keeps a ring buffer of the latest
 * events and invalidates TanStack queries. A dropped stream only flips the status to `reconnecting`;
 * data is never marked as failed because of it.
 */
export function useOrgEvents(orgId: string | undefined): FeedSnapshot {
  const qc = useQueryClient()
  const [store] = useState(() => new EventStore())

  useEffect(() => {
    store.reset()
    if (!orgId) return
    let es: EventSource | null = null
    let timer: ReturnType<typeof setTimeout> | undefined
    let stopped = false
    const batcher = createInvalidationBatcher((ev) => invalidateFor(qc, orgId, ev))
    // Events older than this page are history: the queries loaded on mount already reflect them, so one refresh of
    // the active queries once the replay has settled is enough (a replay can carry hundreds of events).
    const mountedAt = Date.now()
    let replayTimer: ReturnType<typeof setTimeout> | undefined

    const connect = () => {
      const after = readSeq(orgId)
      es = new EventSource(`/api/v1/orgs/${orgId}/events/stream?after=${after}`)
      es.onopen = () => store.setStatus('live')
      es.onerror = () => {
        store.setStatus('reconnecting')
        // Native EventSource retries itself (and sends Last-Event-ID) unless the browser gave up.
        if (es && es.readyState === EventSource.CLOSED && !stopped) {
          es.close()
          timer = setTimeout(connect, RECONNECT_MS)
        }
      }
      es.onmessage = (m: MessageEvent<string>) => {
        let ev: EventDto
        try {
          ev = JSON.parse(m.data) as EventDto
        } catch {
          return
        }
        writeSeq(orgId, ev.seq)
        store.push(ev)
        if (Date.parse(ev.created_at) < mountedAt) {
          clearTimeout(replayTimer)
          replayTimer = setTimeout(() => void qc.invalidateQueries(), REPLAY_SETTLE_MS)
        } else {
          batcher.add(ev)
        }
      }
    }
    connect()
    return () => {
      stopped = true
      batcher.cancel()
      clearTimeout(replayTimer)
      clearTimeout(timer)
      es?.close()
    }
  }, [orgId, qc, store])

  return useSyncExternalStore(store.subscribe, store.getSnapshot)
}
