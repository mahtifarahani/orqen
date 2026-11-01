import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import type { KnowledgeAuthorSessionDto } from '@/api/generated/model'
import { useAuthorSessions, useCancelAuthorSession, useFallbackAuthorSession, useRetryAuthorSession } from '@/api/hooks/knowledge'
import { Ltr } from '@/components/ltr'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { formatDateTime } from '@/lib/format'
import { problemMessage } from '@/lib/problem-message'
import { useIntentKey } from '@/lib/use-intent-key'
import { useLocale } from '@/lib/use-locale'

const TERMINAL = ['applied', 'failed', 'cancelled']

/** Roles-author sessions: the state, the translated reason, and the three actions with the reason they are disabled. */
export function AuthorSessions({ orgId }: { orgId: string }) {
  const { t } = useTranslation()
  const sessions = useAuthorSessions(orgId)
  const items = sessions.data?.items ?? []
  return (
    <section aria-label={t('knowledgePage.author.title')} className="flex flex-col gap-3">
      <h2 className="text-lg font-semibold">{t('knowledgePage.author.title')}</h2>
      <p className="text-sm text-muted-foreground">{t('knowledgePage.author.help')}</p>
      {sessions.isPending && <LoadingBlock rows={2} />}
      {sessions.isError && <LoadFailed title={t('knowledgePage.author.title')} error={sessions.error} onRetry={() => void sessions.refetch()} />}
      {sessions.isSuccess && items.length === 0 && <p className="text-sm text-muted-foreground">{t('knowledgePage.author.none')}</p>}
      <ul className="flex flex-col gap-2">
        {items.map((s) => (
          <SessionRow key={s.id} orgId={orgId} session={s} />
        ))}
      </ul>
    </section>
  )
}

function SessionRow({ orgId, session: s }: { orgId: string; session: KnowledgeAuthorSessionDto }) {
  const { t } = useTranslation()
  const locale = useLocale()
  const [retryKey, renewRetry] = useIntentKey()
  const [fallbackKey, renewFallback] = useIntentKey()
  const [confirm, setConfirm] = useState(false)
  const cancel = useCancelAuthorSession(orgId, s.revision)
  const retry = useRetryAuthorSession(orgId, retryKey)
  const fallback = useFallbackAuthorSession(orgId, fallbackKey)
  const terminal = TERMINAL.includes(s.state)
  const fail = (e: unknown) => toast.error(problemMessage(t, e))
  const whyRetry = s.state === 'failed' ? '' : t('knowledgePage.author.why.retry')
  const whyFallback = terminal ? '' : t('knowledgePage.author.why.fallback')
  const whyCancel = terminal ? t('knowledgePage.author.why.cancel') : ''
  return (
    <li className="flex flex-col gap-2 rounded-md border p-3 text-sm" data-session-state={s.state}>
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant={s.state === 'applied' ? 'default' : s.state === 'failed' ? 'destructive' : 'secondary'}>{t(`knowledgePage.author.state.${s.state}`)}</Badge>
        <Ltr>{s.id}</Ltr>
        <span className="text-muted-foreground">{t('knowledgePage.author.attempt', { n: s.attempt })}</span>
        <span className="text-muted-foreground">{formatDateTime(s.updated_at, locale)}</span>
      </div>
      {s.code && <p className="text-muted-foreground">{t(`knowledgePage.author.code.${s.code}`, { defaultValue: s.code })}</p>}
      {s.state === 'applied' && <p>{t('knowledgePage.author.needsReview')}</p>}
      <div className="flex flex-wrap gap-2">
        <Button
          size="sm"
          variant="outline"
          disabled={s.state !== 'failed' || retry.isPending}
          title={whyRetry}
          aria-label={t('knowledgePage.author.retry')}
          onClick={() => retry.mutate({ orgId, kasId: s.id }, { onSuccess: () => renewRetry(), onError: fail })}
        >
          {t('knowledgePage.author.retry')}
        </Button>
        <Button size="sm" variant="outline" disabled={!terminal || fallback.isPending} title={whyFallback} aria-label={t('knowledgePage.author.fallback')} onClick={() => setConfirm(true)}>
          {t('knowledgePage.author.fallback')}
        </Button>
        <Button size="sm" variant="ghost" disabled={terminal || cancel.isPending} title={whyCancel} aria-label={t('knowledgePage.author.cancel')} onClick={() => cancel.mutate({ orgId, kasId: s.id }, { onError: fail })}>
          {t('knowledgePage.author.cancel')}
        </Button>
      </div>
      {(whyRetry || whyFallback || whyCancel) && (
        <p className="text-xs text-muted-foreground">{[whyRetry, whyFallback, whyCancel].filter(Boolean).join(' / ')}</p>
      )}
      <Dialog open={confirm} onOpenChange={setConfirm}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('knowledgePage.author.fallbackTitle')}</DialogTitle>
            <DialogDescription>{t('knowledgePage.author.fallbackBody')}</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirm(false)}>{t('common.cancel')}</Button>
            <Button
              disabled={fallback.isPending}
              onClick={() =>
                fallback.mutate({ orgId, kasId: s.id }, { onSuccess: () => { renewFallback(); setConfirm(false); toast.success(t('knowledgePage.author.fallbackDone')) }, onError: fail })
              }
            >
              {t('knowledgePage.author.fallbackConfirm')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </li>
  )
}
