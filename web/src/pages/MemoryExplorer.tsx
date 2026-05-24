/**
 * MemoryExplorer — semantic search + knowledge graph visualisation.
 *
 * Route: /memory  (projectID supplied from parent shell)
 *
 * Layout
 * ──────
 * Top: search bar + filters (type, scope, min_trust)
 * Left column: results list with content + metadata cards
 * Right column: knowledge graph — entities as nodes, relations as edges
 *               rendered on an HTML5 canvas (no extra lib dependency)
 *
 * The graph re-fetches whenever the filter changes.
 * Results list uses react-query; the graph is drawn imperatively on a canvas.
 */

import { useCallback, useEffect, useRef, useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import {
  Search, Plus, Shield, Eye, Database,
  Network, Loader2, X, ChevronDown,
} from 'lucide-react'
import {
  api,
  type MemoryRecord, type MemoryEntity, type MemoryRelation,
  type MemoryScope, type MemoryType, type TrustTier,
} from '@/api/client'

// ── Constants ─────────────────────────────────────────────────

const SCOPES: MemoryScope[] = ['private', 'agent', 'workflow', 'project', 'global']
const TYPES: MemoryType[]   = ['working', 'episodic', 'semantic', 'procedural']
const TRUSTS: TrustTier[]   = ['verified', 'observed', 'untrusted']

const trustColor: Record<TrustTier, string> = {
  verified:  'text-success border-success/30 bg-success/10',
  observed:  'text-warning border-warning/30 bg-warning/10',
  untrusted: 'text-danger  border-danger/30  bg-danger/10',
}

const typeColor: Record<MemoryType, string> = {
  working:    'text-accent',
  episodic:   'text-blue-400',
  semantic:   'text-purple-400',
  procedural: 'text-orange-400',
}

// ── Select helper ─────────────────────────────────────────────

function Select<T extends string>({
  label, value, options, onChange,
}: {
  label: string
  value: T | ''
  options: T[]
  onChange: (v: T | '') => void
}) {
  return (
    <div className="relative">
      <select
        value={value}
        onChange={e => onChange(e.target.value as T | '')}
        className="appearance-none bg-canvas-elevated border border-canvas-border rounded-lg pl-3 pr-7 py-1.5 text-xs text-slate-300 focus:outline-none focus:border-accent"
      >
        <option value="">{label}</option>
        {options.map(o => (
          <option key={o} value={o}>{o}</option>
        ))}
      </select>
      <ChevronDown className="pointer-events-none absolute right-2 top-1/2 -translate-y-1/2 w-3 h-3 text-muted" />
    </div>
  )
}

// ── Memory card ───────────────────────────────────────────────

function MemoryCard({ record }: { record: MemoryRecord }) {
  const [expanded, setExpanded] = useState(false)
  const preview = record.content.slice(0, 160)
  const needsTruncate = record.content.length > 160

  return (
    <div className="bg-canvas-elevated border border-canvas-border rounded-xl p-4 space-y-2">
      <div className="flex items-start gap-2">
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2 mb-1 flex-wrap">
            <span className={`text-xs font-mono ${typeColor[record.memory_type] ?? 'text-muted'}`}>
              {record.memory_type}
            </span>
            <span className="text-xs text-muted">{record.scope}</span>
            <span className={`text-xs px-1.5 py-0.5 rounded border ${trustColor[record.trust_tier] ?? ''}`}>
              {record.trust_tier}
            </span>
            {record.is_quarantined && (
              <span className="text-xs px-1.5 py-0.5 rounded border border-danger/30 bg-danger/10 text-danger">
                quarantined
              </span>
            )}
          </div>
          <p className="text-sm text-slate-300 leading-relaxed">
            {expanded ? record.content : preview}
            {needsTruncate && !expanded && '…'}
          </p>
          {needsTruncate && (
            <button
              onClick={() => setExpanded(e => !e)}
              className="text-xs text-accent hover:text-accent-hover mt-1"
            >
              {expanded ? 'Show less' : 'Show more'}
            </button>
          )}
        </div>
      </div>
      <div className="flex items-center gap-3 text-xs text-muted border-t border-canvas-border pt-2">
        <div className="flex items-center gap-1">
          <Eye className="w-3 h-3" />
          {record.access_count} reads
        </div>
        <div className="flex items-center gap-1">
          <Shield className="w-3 h-3" />
          {(record.confidence * 100).toFixed(0)}% confidence
        </div>
        <span className="ml-auto font-mono">{new Date(record.created_at).toLocaleDateString()}</span>
      </div>
    </div>
  )
}

// ── Write memory modal ────────────────────────────────────────

function WriteMemoryModal({
  projectID,
  onDone,
  onCancel,
}: {
  projectID: string
  onDone: () => void
  onCancel: () => void
}) {
  const [content, setContent] = useState('')
  const [type, setType] = useState<MemoryType>('semantic')
  const [scope, setScope] = useState<MemoryScope>('project')
  const [trust, setTrust] = useState<TrustTier>('verified')
  const qc = useQueryClient()

  const mut = useMutation({
    mutationFn: () =>
      api.writeMemory(projectID, {
        content,
        type,
        scope,
        trust_tier: trust,
        confidence: trust === 'verified' ? 1.0 : trust === 'observed' ? 0.7 : 0.4,
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['memory', projectID] })
      onDone()
    },
  })

  return (
    <div className="fixed inset-0 bg-black/60 flex items-center justify-center z-50">
      <div className="bg-canvas-elevated border border-canvas-border rounded-xl p-6 w-[480px] animate-fade-in space-y-4">
        <div className="flex items-center justify-between">
          <h3 className="text-sm font-semibold text-white">Write Memory</h3>
          <button onClick={onCancel} className="text-muted hover:text-slate-300">
            <X className="w-4 h-4" />
          </button>
        </div>

        <textarea
          value={content}
          onChange={e => setContent(e.target.value)}
          placeholder="Memory content…"
          rows={4}
          autoFocus
          className="w-full bg-canvas border border-canvas-border rounded-lg px-3 py-2 text-sm text-slate-200 placeholder-muted focus:outline-none focus:border-accent resize-none"
        />

        <div className="flex gap-3 flex-wrap">
          <Select label="Type"  value={type}  options={TYPES}   onChange={v => v && setType(v as MemoryType)} />
          <Select label="Scope" value={scope} options={SCOPES}  onChange={v => v && setScope(v as MemoryScope)} />
          <Select label="Trust" value={trust} options={TRUSTS}  onChange={v => v && setTrust(v as TrustTier)} />
        </div>

        <div className="flex gap-2 justify-end">
          <button onClick={onCancel} className="text-sm text-muted hover:text-slate-300 px-3 py-1.5">Cancel</button>
          <button
            onClick={() => mut.mutate()}
            disabled={!content.trim() || mut.isPending}
            className="text-sm bg-accent hover:bg-accent-hover disabled:opacity-40 text-white px-4 py-1.5 rounded-lg transition-colors"
          >
            {mut.isPending ? 'Writing…' : 'Write'}
          </button>
        </div>
      </div>
    </div>
  )
}

// ── Knowledge graph canvas ────────────────────────────────────

interface GraphNode {
  id: string
  label: string
  type: string
  x: number
  y: number
  vx: number
  vy: number
}

interface GraphEdge {
  from: string
  to: string
  label: string
}

function KnowledgeGraph({
  entities,
  relations,
}: {
  entities: MemoryEntity[]
  relations: MemoryRelation[]
}) {
  const canvasRef = useRef<HTMLCanvasElement>(null)
  const nodesRef = useRef<GraphNode[]>([])
  const rafRef   = useRef<number>(0)
  const dragRef  = useRef<{ node: GraphNode; offX: number; offY: number } | null>(null)

  // ── Build graph nodes + edges from entities/relations ─────
  useEffect(() => {
    const canvas = canvasRef.current
    if (!canvas) return
    const W = canvas.clientWidth || 500
    const H = canvas.clientHeight || 400
    canvas.width  = W
    canvas.height = H

    // Place nodes in a rough circle so they don't all start at origin
    nodesRef.current = entities.map((e, i) => {
      const angle = (2 * Math.PI * i) / Math.max(entities.length, 1)
      const r = Math.min(W, H) * 0.32
      return {
        id:    e.id,
        label: e.name,
        type:  e.entity_type,
        x: W / 2 + r * Math.cos(angle),
        y: H / 2 + r * Math.sin(angle),
        vx: 0,
        vy: 0,
      }
    })
  }, [entities])

  // ── Force-directed layout + draw loop ────────────────────
  useEffect(() => {
    const canvas = canvasRef.current
    if (!canvas) return
    const ctx = canvas.getContext('2d')
    if (!ctx) return

    const edges: GraphEdge[] = relations.map(r => ({
      from:  r.from_entity_id,
      to:    r.to_entity_id,
      label: r.relation_type,
    }))

    const REPULSION  = 3500
    const ATTRACTION = 0.04
    const DAMPING    = 0.85
    const REST_LEN   = 120

    function tick() {
      const nodes = nodesRef.current
      const W = canvas.width
      const H = canvas.height

      // Repulsion between all pairs
      for (let i = 0; i < nodes.length; i++) {
        for (let j = i + 1; j < nodes.length; j++) {
          const dx = nodes[j].x - nodes[i].x
          const dy = nodes[j].y - nodes[i].y
          const dist = Math.sqrt(dx * dx + dy * dy) || 1
          const force = REPULSION / (dist * dist)
          const fx = (dx / dist) * force
          const fy = (dy / dist) * force
          nodes[i].vx -= fx
          nodes[i].vy -= fy
          nodes[j].vx += fx
          nodes[j].vy += fy
        }
      }

      // Spring attraction along edges
      const nodeMap = new Map(nodes.map(n => [n.id, n]))
      for (const edge of edges) {
        const a = nodeMap.get(edge.from)
        const b = nodeMap.get(edge.to)
        if (!a || !b) continue
        const dx   = b.x - a.x
        const dy   = b.y - a.y
        const dist = Math.sqrt(dx * dx + dy * dy) || 1
        const force = ATTRACTION * (dist - REST_LEN)
        const fx = (dx / dist) * force
        const fy = (dy / dist) * force
        a.vx += fx; a.vy += fy
        b.vx -= fx; b.vy -= fy
      }

      // Integrate + damp + clamp to canvas
      for (const n of nodes) {
        if (dragRef.current?.node === n) continue
        n.vx *= DAMPING
        n.vy *= DAMPING
        n.x = Math.max(40, Math.min(W - 40, n.x + n.vx))
        n.y = Math.max(20, Math.min(H - 20, n.y + n.vy))
      }

      draw(ctx, nodes, edges, W, H)
      rafRef.current = requestAnimationFrame(tick)
    }

    rafRef.current = requestAnimationFrame(tick)
    return () => cancelAnimationFrame(rafRef.current)
  }, [relations])

  function draw(
    ctx: CanvasRenderingContext2D,
    nodes: GraphNode[],
    edges: GraphEdge[],
    W: number,
    H: number,
  ) {
    ctx.clearRect(0, 0, W, H)

    // Edges
    const nodeMap = new Map(nodes.map(n => [n.id, n]))
    ctx.strokeStyle = '#2a2f42'
    ctx.lineWidth   = 1.5
    ctx.font        = '9px monospace'
    ctx.fillStyle   = '#64748b'
    for (const edge of edges) {
      const a = nodeMap.get(edge.from)
      const b = nodeMap.get(edge.to)
      if (!a || !b) continue
      ctx.beginPath()
      ctx.moveTo(a.x, a.y)
      ctx.lineTo(b.x, b.y)
      ctx.stroke()
      // Edge label at midpoint
      const mx = (a.x + b.x) / 2
      const my = (a.y + b.y) / 2
      ctx.fillText(edge.label, mx + 4, my - 4)
    }

    // Nodes
    const PALETTE = ['#6366f1', '#22c55e', '#f59e0b', '#ef4444', '#a78bfa', '#34d399']
    const typeIndex = new Map<string, number>()
    let colorIdx = 0
    for (const n of nodes) {
      if (!typeIndex.has(n.type)) typeIndex.set(n.type, colorIdx++ % PALETTE.length)
    }

    for (const n of nodes) {
      const color = PALETTE[typeIndex.get(n.type) ?? 0]
      // Node circle
      ctx.beginPath()
      ctx.arc(n.x, n.y, 18, 0, 2 * Math.PI)
      ctx.fillStyle = color + '28'  // 16% opacity fill
      ctx.fill()
      ctx.strokeStyle = color
      ctx.lineWidth = 2
      ctx.stroke()

      // Type abbreviation inside
      ctx.fillStyle = color
      ctx.font = 'bold 9px monospace'
      ctx.textAlign = 'center'
      ctx.textBaseline = 'middle'
      ctx.fillText(n.type.slice(0, 3).toUpperCase(), n.x, n.y)

      // Label below
      ctx.fillStyle = '#cbd5e1'
      ctx.font = '10px sans-serif'
      ctx.textAlign = 'center'
      ctx.textBaseline = 'top'
      const label = n.label.length > 16 ? n.label.slice(0, 14) + '…' : n.label
      ctx.fillText(label, n.x, n.y + 22)
    }
  }

  // ── Drag support ──────────────────────────────────────────
  function hitTest(x: number, y: number): GraphNode | null {
    for (const n of nodesRef.current) {
      const dx = n.x - x
      const dy = n.y - y
      if (dx * dx + dy * dy <= 18 * 18) return n
    }
    return null
  }

  function onMouseDown(e: React.MouseEvent<HTMLCanvasElement>) {
    const rect = canvasRef.current!.getBoundingClientRect()
    const x = e.clientX - rect.left
    const y = e.clientY - rect.top
    const node = hitTest(x, y)
    if (node) dragRef.current = { node, offX: node.x - x, offY: node.y - y }
  }

  function onMouseMove(e: React.MouseEvent<HTMLCanvasElement>) {
    if (!dragRef.current) return
    const rect = canvasRef.current!.getBoundingClientRect()
    dragRef.current.node.x = e.clientX - rect.left + dragRef.current.offX
    dragRef.current.node.y = e.clientY - rect.top  + dragRef.current.offY
    dragRef.current.node.vx = 0
    dragRef.current.node.vy = 0
  }

  function onMouseUp() {
    dragRef.current = null
  }

  if (entities.length === 0) {
    return (
      <div className="flex-1 flex flex-col items-center justify-center gap-3 text-muted">
        <Network className="w-8 h-8" />
        <span className="text-sm">No entities in graph</span>
        <span className="text-xs text-center max-w-[200px]">
          Entities are extracted automatically as memories accumulate
        </span>
      </div>
    )
  }

  return (
    <canvas
      ref={canvasRef}
      className="flex-1 w-full h-full cursor-grab active:cursor-grabbing"
      onMouseDown={onMouseDown}
      onMouseMove={onMouseMove}
      onMouseUp={onMouseUp}
      onMouseLeave={onMouseUp}
    />
  )
}

// ── Main page ─────────────────────────────────────────────────

export default function MemoryExplorer({ projectID }: { projectID: string | null }) {
  const [query, setQuery]     = useState('')
  const [typeF, setTypeF]     = useState<MemoryType | ''>('')
  const [scopeF, setScopeF]   = useState<MemoryScope | ''>('')
  const [trustF, setTrustF]   = useState<TrustTier | ''>('')
  const [tab, setTab]         = useState<'search' | 'graph'>('search')
  const [showWrite, setShowWrite] = useState(false)
  const [debouncedQ, setDebouncedQ] = useState('')

  // Debounce search input (400 ms)
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  function handleQueryChange(v: string) {
    setQuery(v)
    if (debounceRef.current) clearTimeout(debounceRef.current)
    debounceRef.current = setTimeout(() => setDebouncedQ(v), 400)
  }

  // ── Memory search query ──────────────────────────────────
  const memQ = useQuery({
    queryKey: ['memory', projectID, debouncedQ, typeF, scopeF, trustF],
    queryFn: () =>
      api.queryMemory(projectID!, {
        q:         debouncedQ || undefined,
        type:      typeF   || undefined,
        scope:     scopeF  || undefined,
        min_trust: trustF  || undefined,
      }),
    enabled: !!projectID,
  })
  const records: MemoryRecord[] = memQ.data?.results ?? []

  // ── Knowledge graph query ────────────────────────────────
  const graphQ = useQuery({
    queryKey: ['memory-graph', projectID],
    queryFn: () => api.getKnowledgeGraph(projectID!, { depth: 2 }),
    enabled: !!projectID && tab === 'graph',
    staleTime: 30_000,
  })

  const clearFilters = useCallback(() => {
    setQuery(''); setDebouncedQ(''); setTypeF(''); setScopeF(''); setTrustF('')
  }, [])

  if (!projectID) {
    return (
      <div className="flex-1 flex items-center justify-center text-muted text-sm">
        Select a project to explore memory.
      </div>
    )
  }

  const hasFilters = query || typeF || scopeF || trustF

  return (
    <div className="flex-1 flex flex-col overflow-hidden">
      {showWrite && (
        <WriteMemoryModal
          projectID={projectID}
          onDone={() => setShowWrite(false)}
          onCancel={() => setShowWrite(false)}
        />
      )}

      {/* Top bar */}
      <div className="flex items-center gap-3 px-4 py-3 border-b border-canvas-border bg-canvas-surface flex-shrink-0">
        {/* Search input */}
        <div className="relative flex-1 max-w-xl">
          <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-muted pointer-events-none" />
          <input
            value={query}
            onChange={e => handleQueryChange(e.target.value)}
            placeholder="Semantic search…"
            className="w-full bg-canvas-elevated border border-canvas-border rounded-lg pl-8 pr-3 py-1.5 text-sm text-slate-200 placeholder-muted focus:outline-none focus:border-accent"
          />
          {query && (
            <button
              onClick={clearFilters}
              className="absolute right-2.5 top-1/2 -translate-y-1/2 text-muted hover:text-slate-300"
            >
              <X className="w-3 h-3" />
            </button>
          )}
        </div>

        {/* Filters */}
        <Select label="Type"  value={typeF}  options={TYPES}   onChange={setTypeF}  />
        <Select label="Scope" value={scopeF} options={SCOPES}  onChange={setScopeF} />
        <Select label="Trust" value={trustF} options={TRUSTS}  onChange={setTrustF} />

        {hasFilters && (
          <button onClick={clearFilters} className="text-xs text-muted hover:text-slate-300">
            Clear
          </button>
        )}

        <div className="flex-1" />

        {/* Tabs */}
        <div className="flex items-center gap-1 bg-canvas-elevated border border-canvas-border rounded-lg p-0.5">
          <button
            onClick={() => setTab('search')}
            className={`flex items-center gap-1 px-3 py-1 text-xs rounded-md transition-colors ${tab === 'search' ? 'bg-canvas-border text-slate-200' : 'text-muted hover:text-slate-300'}`}
          >
            <Database className="w-3 h-3" />
            Records
          </button>
          <button
            onClick={() => setTab('graph')}
            className={`flex items-center gap-1 px-3 py-1 text-xs rounded-md transition-colors ${tab === 'graph' ? 'bg-canvas-border text-slate-200' : 'text-muted hover:text-slate-300'}`}
          >
            <Network className="w-3 h-3" />
            Graph
          </button>
        </div>

        {/* Write button */}
        <button
          onClick={() => setShowWrite(true)}
          className="flex items-center gap-1.5 text-xs px-3 py-1.5 rounded-lg bg-accent/15 border border-accent/30 hover:bg-accent/25 text-accent transition-colors"
        >
          <Plus className="w-3.5 h-3.5" />
          Write
        </button>
      </div>

      {/* Body */}
      {tab === 'search' ? (
        <div className="flex-1 overflow-y-auto p-4">
          {memQ.isLoading ? (
            <div className="flex items-center justify-center h-40">
              <Loader2 className="w-5 h-5 text-muted animate-spin" />
            </div>
          ) : records.length === 0 ? (
            <div className="flex flex-col items-center justify-center h-40 gap-3 text-muted">
              <Database className="w-6 h-6" />
              <span className="text-sm">{hasFilters ? 'No results match your filters' : 'No memory records yet'}</span>
            </div>
          ) : (
            <div className="grid grid-cols-1 xl:grid-cols-2 gap-3 max-w-5xl">
              <div className="col-span-full flex items-center gap-2 mb-1">
                <span className="text-xs text-muted">
                  {memQ.data?.count ?? records.length} record{records.length !== 1 ? 's' : ''}
                  {hasFilters && ' matching filters'}
                </span>
              </div>
              {records.map(r => (
                <MemoryCard key={r.id} record={r} />
              ))}
            </div>
          )}
        </div>
      ) : (
        <div className="flex-1 flex flex-col overflow-hidden relative">
          {graphQ.isLoading && (
            <div className="absolute inset-0 flex items-center justify-center bg-canvas/50 z-10">
              <Loader2 className="w-6 h-6 text-muted animate-spin" />
            </div>
          )}
          {/* Graph legend */}
          {graphQ.data && graphQ.data.entities.length > 0 && (
            <div className="flex-shrink-0 px-4 py-2 border-b border-canvas-border flex items-center gap-3">
              <span className="text-xs text-muted">
                {graphQ.data.entities.length} entities · {graphQ.data.relations.length} relations
              </span>
              <span className="text-xs text-muted/60">Drag nodes to rearrange</span>
            </div>
          )}
          <KnowledgeGraph
            entities={graphQ.data?.entities ?? []}
            relations={graphQ.data?.relations ?? []}
          />
        </div>
      )}
    </div>
  )
}
