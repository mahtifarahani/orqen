import { lazy, Suspense, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useParams } from 'react-router'
import { toast } from 'sonner'
import type { GraphEdge } from '@/api/generated/model'
import { useConfirmEdge, useGraphNodes, useNeighborhood, useRebuild, useRejectEdge } from '@/api/hooks/graph'
import { Ltr } from '@/components/ltr'
import { LoadFailed, LoadingBlock } from '@/components/query-state'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { problemMessage } from '@/lib/problem-message'
import { CodeGraphPanel } from './code-panel'

const GraphCanvas = lazy(() => import('./graph-canvas'))

export function GraphPage() {
  const { t } = useTranslation()
  const { orgId = '' } = useParams()
  const [q, setQ] = useState('')
  const [node, setNode] = useState<string | undefined>()
  const [depth, setDepth] = useState(1)
  const nodes = useGraphNodes(orgId, q)
  const hood = useNeighborhood(orgId, node, depth)
  const rebuild = useRebuild(orgId)
  const fail = (e: unknown) => toast.error(problemMessage(t, e))

  return (
    <div className="flex flex-col gap-6">
      <header className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold">{t('graphPage.title')}</h1>
          <p className="text-sm text-muted-foreground">{t('graphPage.subtitle')}</p>
        </div>
        <Button variant="outline" disabled={rebuild.isPending} onClick={() => rebuild.mutate({ orgId }, { onSuccess: () => toast.success(t('graphPage.rebuildStarted')), onError: fail })}>
          {t('graphPage.rebuild')}
        </Button>
      </header>
      <div className="grid gap-4 lg:grid-cols-[280px_1fr]">
        <section className="flex flex-col gap-2" aria-label={t('graphPage.nodes')}>
          <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t('graphPage.search')} aria-label={t('graphPage.search')} />
          {nodes.isPending && <LoadingBlock />}
          {nodes.isError && <LoadFailed title={t('graphPage.nodes')} error={nodes.error} onRetry={() => nodes.refetch()} />}
          {nodes.data && nodes.data.items.length === 0 && <p className="text-sm text-muted-foreground">{t('graphPage.noNodes')}</p>}
          <ul className="flex max-h-[420px] flex-col gap-1 overflow-auto">
            {nodes.data?.items.map((n) => (
              <li key={n.id}>
                <Button variant={n.id === node ? 'secondary' : 'ghost'} className="w-full justify-start gap-2" onClick={() => setNode(n.id)}>
                  <Badge variant="outline">{n.kind}</Badge>
                  <Ltr className="truncate">{n.label}</Ltr>
                </Button>
              </li>
            ))}
          </ul>
        </section>
        <section className="flex flex-col gap-3">
          {!node && <p className="text-sm text-muted-foreground">{t('graphPage.pick')}</p>}
          {node && (
            <div className="flex items-center gap-2">
              <span className="text-sm">{t('graphPage.depth')}</span>
              {[1, 2].map((d) => (
                <Button key={d} size="sm" variant={depth === d ? 'secondary' : 'outline'} onClick={() => setDepth(d)}>
                  {d}
                </Button>
              ))}
            </div>
          )}
          {hood.isPending && node && <LoadingBlock />}
          {hood.isError && <LoadFailed title={t('graphPage.edges')} error={hood.error} onRetry={() => hood.refetch()} />}
          {hood.data && (
            <>
              {hood.data.truncated && <Badge variant="outline">{t('graphPage.truncated')}</Badge>}
              <Suspense fallback={<LoadingBlock />}>
                <GraphCanvas data={hood.data} onSelect={setNode} />
              </Suspense>
              <h2 className="text-lg font-medium">{t('graphPage.edges')}</h2>
              {hood.data.edges.length === 0 && <p className="text-sm text-muted-foreground">{t('graphPage.noEdges')}</p>}
              <ul className="flex flex-col gap-2">
                {hood.data.edges.map((e) => (
                  <EdgeRow key={e.id} orgId={orgId} edge={e} label={(id) => hood.data.nodes.find((n) => n.id === id)?.label ?? id} fail={fail} />
                ))}
              </ul>
            </>
          )}
        </section>
      </div>
      <CodeGraphPanel orgId={orgId} />
    </div>
  )
}

function EdgeRow({ orgId, edge, label, fail }: { orgId: string; edge: GraphEdge; label: (id: string) => string; fail: (e: unknown) => void }) {
  const { t } = useTranslation()
  const confirm = useConfirmEdge(orgId, edge.revision)
  const reject = useRejectEdge(orgId, edge.revision)
  const decided = edge.status === 'verified' || edge.status === 'rejected'
  return (
    <li>
      <Card>
        <CardContent className="flex flex-wrap items-center justify-between gap-3 py-3">
          <div className="flex flex-wrap items-center gap-2 text-sm">
            <Ltr>{label(edge.from)}</Ltr>
            <Badge variant="outline">{edge.relation}</Badge>
            <Ltr>{label(edge.to)}</Ltr>
            <Badge>{t(`graphPage.status.${edge.status}`, { defaultValue: edge.status })}</Badge>
            <span className="text-muted-foreground">{Math.round(edge.confidence * 100)}%</span>
          </div>
          {!decided && (
            <div className="flex gap-2">
              <Button size="sm" disabled={confirm.isPending} onClick={() => confirm.mutate({ orgId, gedId: edge.id }, { onSuccess: () => toast.success(t('graphPage.confirmed')), onError: fail })}>
                {t('graphPage.confirm')}
              </Button>
              <Button size="sm" variant="outline" disabled={reject.isPending} onClick={() => reject.mutate({ orgId, gedId: edge.id }, { onSuccess: () => toast.success(t('graphPage.rejected')), onError: fail })}>
                {t('graphPage.reject')}
              </Button>
            </div>
          )}
        </CardContent>
      </Card>
    </li>
  )
}
