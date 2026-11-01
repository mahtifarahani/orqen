import '@xyflow/react/dist/style.css'
import { Background, Controls, ReactFlow, type Edge, type Node } from '@xyflow/react'
import type { GraphNeighborhood } from '@/api/generated/model'
import { useTheme } from '@/components/theme-provider'

/** React Flow canvas, lazy-loaded; always left-to-right (the textual edge list carries the accessible content). */
export default function GraphCanvas({ data, onSelect }: { data: GraphNeighborhood; onSelect: (node: string) => void }) {
  const { resolved } = useTheme()
  const nodes: Node[] = data.nodes.map((n, i) => ({
    id: n.id,
    data: { label: n.label },
    position: { x: (i % 5) * 200, y: Math.floor(i / 5) * 110 },
    style: { background: 'var(--card)', color: 'var(--card-foreground)', borderColor: 'var(--border)', ...(n.id === data.start ? { borderWidth: 2, borderColor: 'var(--ring)' } : {}) },
  }))
  const edges: Edge[] = data.edges.map((e) => ({
    id: e.id,
    source: e.from,
    target: e.to,
    label: e.relation,
    animated: e.status === 'inferred',
    style: e.status === 'rejected' ? { strokeDasharray: '4 4', opacity: 0.4 } : undefined,
  }))
  return (
    <div dir="ltr" className="h-[420px] rounded-md border">
      <ReactFlow nodes={nodes} edges={edges} colorMode={resolved} fitView nodesConnectable={false} onNodeClick={(_, n) => onSelect(n.id)}>
        <Background />
        <Controls showInteractive={false} />
      </ReactFlow>
    </div>
  )
}
