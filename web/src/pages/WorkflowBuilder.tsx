import { useCallback, useEffect, useState } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import {
  ReactFlow, Background, Controls, MiniMap,
  addEdge, useNodesState, useEdgesState,
  type Node, type Edge, type Connection,
  BackgroundVariant,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { Plus, Save, Play, ChevronRight, Loader2, X } from 'lucide-react'
import { api, type Workflow, type WorkflowNode, type WorkflowEdge } from '@/api/client'
import AgentNode from '@/nodes/AgentNode'
import ConditionNode from '@/nodes/ConditionNode'
import ApprovalNode from '@/nodes/ApprovalNode'
import TriggerNode from '@/nodes/TriggerNode'

// ── Node type registry ────────────────────────────────────────

const nodeTypes = {
  agent:     AgentNode,
  condition: ConditionNode,
  approval:  ApprovalNode,
  trigger:   TriggerNode,
}

// ── Palette item ──────────────────────────────────────────────

const PALETTE_ITEMS = [
  { kind: 'trigger',   label: 'Trigger',   color: 'border-success/50',  description: 'Cron, webhook, or manual' },
  { kind: 'agent',     label: 'Agent',     color: 'border-accent/50',   description: 'LLM-powered agent step' },
  { kind: 'condition', label: 'Condition', color: 'border-warning/50',  description: 'Branch on key==value' },
  { kind: 'approval',  label: 'Approval',  color: 'border-muted/50',    description: 'Human sign-off gate' },
]

// ── Conversion helpers ────────────────────────────────────────

function defToFlow(def: { nodes: WorkflowNode[]; edges: WorkflowEdge[] }) {
  const nodes: Node[] = (def.nodes ?? []).map(n => ({
    id: n.id,
    type: n.kind,
    position: n.position ?? { x: 0, y: 0 },
    data: { label: n.label, ...n.config },
  }))
  const edges: Edge[] = (def.edges ?? []).map(e => ({
    id: e.id,
    source: e.source,
    target: e.target,
    label: e.condition,
  }))
  return { nodes, edges }
}

function flowToDef(nodes: Node[], edges: Edge[]): { nodes: WorkflowNode[]; edges: WorkflowEdge[] } {
  return {
    nodes: nodes.map(n => ({
      id: n.id,
      kind: n.type ?? 'agent',
      label: (n.data as { label: string }).label ?? '',
      config: n.data as Record<string, unknown>,
      position: n.position,
    })),
    edges: edges.map(e => ({
      id: e.id,
      source: e.source,
      target: e.target,
      condition: typeof e.label === 'string' ? e.label : undefined,
    })),
  }
}

// ── Inspector panel ───────────────────────────────────────────

function Inspector({
  node,
  onUpdate,
  onClose,
}: {
  node: Node
  onUpdate: (id: string, data: Record<string, unknown>) => void
  onClose: () => void
}) {
  const [label, setLabel] = useState((node.data as { label: string }).label ?? '')

  function save() {
    onUpdate(node.id, { ...(node.data as Record<string, unknown>), label })
  }

  return (
    <div className="w-60 flex-shrink-0 bg-canvas-surface border-l border-canvas-border flex flex-col">
      <div className="flex items-center justify-between px-4 py-3 border-b border-canvas-border">
        <span className="text-xs font-medium text-slate-300">
          {String(node.type ?? 'node')} properties
        </span>
        <button onClick={onClose} className="text-muted hover:text-slate-300">
          <X className="w-3.5 h-3.5" />
        </button>
      </div>
      <div className="flex-1 overflow-y-auto p-4 space-y-4">
        <div>
          <label className="block text-xs text-muted mb-1">Label</label>
          <input
            value={label}
            onChange={e => setLabel(e.target.value)}
            onBlur={save}
            className="w-full bg-canvas-elevated border border-canvas-border rounded px-2 py-1.5 text-sm text-slate-200 focus:outline-none focus:border-accent"
          />
        </div>
        <div>
          <label className="block text-xs text-muted mb-1">Node ID</label>
          <p className="text-xs font-mono text-muted break-all">{node.id}</p>
        </div>
      </div>
    </div>
  )
}

// ── WorkflowList sidebar ──────────────────────────────────────

function WorkflowList({
  projectID,
  activeID,
  onSelect,
  onCreate,
}: {
  projectID: string
  activeID: string | null
  onSelect: (wf: Workflow) => void
  onCreate: () => void
}) {
  const { data } = useQuery({
    queryKey: ['workflows', projectID],
    queryFn: () => api.listWorkflows(projectID),
  })
  const workflows = data?.workflows ?? []

  return (
    <div className="w-52 flex-shrink-0 border-r border-canvas-border flex flex-col">
      <div className="flex items-center justify-between px-3 py-3 border-b border-canvas-border">
        <span className="text-xs font-medium text-muted uppercase tracking-wider">Workflows</span>
        <button onClick={onCreate} className="text-muted hover:text-accent transition-colors">
          <Plus className="w-4 h-4" />
        </button>
      </div>
      <div className="flex-1 overflow-y-auto py-1">
        {workflows.length === 0 && (
          <p className="px-3 py-4 text-xs text-muted text-center">No workflows yet</p>
        )}
        {workflows.map(wf => (
          <button
            key={wf.id}
            onClick={() => onSelect(wf)}
            className={`w-full text-left flex items-center gap-1.5 px-3 py-2 text-sm transition-colors ${wf.id === activeID ? 'text-accent-hover bg-accent/10' : 'text-slate-400 hover:text-slate-200 hover:bg-canvas-elevated'}`}
          >
            <ChevronRight className="w-3 h-3 flex-shrink-0" />
            <span className="truncate">{wf.name}</span>
          </button>
        ))}
      </div>
    </div>
  )
}

// ── New workflow modal ────────────────────────────────────────

function NewWorkflowModal({
  projectID,
  onCreated,
  onCancel,
}: {
  projectID: string
  onCreated: (wf: Workflow) => void
  onCancel: () => void
}) {
  const [name, setName] = useState('')
  const qc = useQueryClient()
  const mut = useMutation({
    mutationFn: () =>
      api.createWorkflow(projectID, {
        name,
        slug: name.toLowerCase().replace(/[^a-z0-9]+/g, '-'),
        definition: { nodes: [], edges: [] },
      }),
    onSuccess: (wf) => {
      qc.invalidateQueries({ queryKey: ['workflows', projectID] })
      onCreated(wf)
    },
  })

  return (
    <div className="fixed inset-0 bg-black/60 flex items-center justify-center z-50">
      <div className="bg-canvas-elevated border border-canvas-border rounded-xl p-6 w-80 animate-fade-in">
        <h3 className="text-sm font-semibold text-white mb-4">New Workflow</h3>
        <input
          value={name}
          onChange={e => setName(e.target.value)}
          placeholder="Workflow name"
          className="w-full bg-canvas border border-canvas-border rounded-lg px-3 py-2 text-sm text-slate-200 placeholder-muted focus:outline-none focus:border-accent mb-4"
          autoFocus
          onKeyDown={e => e.key === 'Enter' && name && mut.mutate()}
        />
        <div className="flex gap-2 justify-end">
          <button onClick={onCancel} className="text-sm text-muted hover:text-slate-300 px-3 py-1.5">Cancel</button>
          <button
            onClick={() => mut.mutate()}
            disabled={!name || mut.isPending}
            className="text-sm bg-accent hover:bg-accent-hover disabled:opacity-40 text-white px-4 py-1.5 rounded-lg transition-colors"
          >
            {mut.isPending ? 'Creating…' : 'Create'}
          </button>
        </div>
      </div>
    </div>
  )
}

// ── Main page ─────────────────────────────────────────────────

let nodeCounter = 100

export default function WorkflowBuilder({ projectID }: { projectID: string | null }) {
  const { workflowID: routeWorkflowID } = useParams()
  const navigate = useNavigate()
  const qc = useQueryClient()

  const [activeWorkflowID, setActiveWorkflowID] = useState<string | null>(routeWorkflowID ?? null)
  const [showNew, setShowNew] = useState(false)
  const [selectedNode, setSelectedNode] = useState<Node | null>(null)

  const [nodes, setNodes, onNodesChange] = useNodesState([])
  const [edges, setEdges, onEdgesChange] = useEdgesState([])

  // Load workflow definition when active changes
  const workflowQ = useQuery({
    queryKey: ['workflow', activeWorkflowID],
    queryFn: () => api.getWorkflow(projectID!, activeWorkflowID!),
    enabled: !!projectID && !!activeWorkflowID,
  })

  useEffect(() => {
    if (workflowQ.data) {
      const { nodes: n, edges: e } = defToFlow(workflowQ.data.definition)
      setNodes(n)
      setEdges(e)
    }
  }, [workflowQ.data, setNodes, setEdges])

  // Save mutation
  const saveMut = useMutation({
    mutationFn: () =>
      api.updateWorkflow(projectID!, activeWorkflowID!, {
        definition: flowToDef(nodes, edges),
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['workflow', activeWorkflowID] }),
  })

  // Start run mutation
  const runMut = useMutation({
    mutationFn: () => api.startWorkflowRun(projectID!, activeWorkflowID!),
    onSuccess: (run) => navigate(`/traces/${run.id}`),
  })

  const onConnect = useCallback(
    (params: Connection) => setEdges(eds => addEdge(params, eds)),
    [setEdges],
  )

  function addNode(kind: string) {
    const id = `${kind}-${++nodeCounter}`
    const newNode: Node = {
      id,
      type: kind,
      position: { x: 200 + Math.random() * 100, y: 150 + Math.random() * 100 },
      data: { label: `${kind} ${nodeCounter}` },
    }
    setNodes(ns => [...ns, newNode])
  }

  function updateNodeData(id: string, data: Record<string, unknown>) {
    setNodes(ns => ns.map(n => n.id === id ? { ...n, data } : n))
  }

  if (!projectID) {
    return (
      <div className="flex-1 flex items-center justify-center text-muted text-sm">
        Select a project to build workflows.
      </div>
    )
  }

  return (
    <div className="flex-1 flex overflow-hidden">
      {showNew && (
        <NewWorkflowModal
          projectID={projectID}
          onCreated={wf => { setActiveWorkflowID(wf.id); setShowNew(false) }}
          onCancel={() => setShowNew(false)}
        />
      )}

      {/* Workflow list */}
      <WorkflowList
        projectID={projectID}
        activeID={activeWorkflowID}
        onSelect={wf => setActiveWorkflowID(wf.id)}
        onCreate={() => setShowNew(true)}
      />

      {/* Canvas area */}
      {!activeWorkflowID ? (
        <div className="flex-1 flex items-center justify-center text-muted text-sm">
          Select or create a workflow.
        </div>
      ) : (
        <div className="flex-1 flex flex-col overflow-hidden">
          {/* Toolbar */}
          <div className="flex items-center gap-2 px-4 py-2 border-b border-canvas-border bg-canvas-surface">
            <span className="text-sm font-medium text-slate-300 mr-2">
              {workflowQ.data?.name ?? '…'}
              {workflowQ.data && (
                <span className="ml-2 text-xs text-muted">v{workflowQ.data.version}</span>
              )}
            </span>

            {/* Node palette */}
            {PALETTE_ITEMS.map(item => (
              <button
                key={item.kind}
                onClick={() => addNode(item.kind)}
                title={item.description}
                className={`flex items-center gap-1 text-xs px-2.5 py-1 rounded-lg border ${item.color} bg-canvas-elevated hover:bg-canvas-border/50 text-slate-300 transition-colors`}
              >
                <Plus className="w-3 h-3" />
                {item.label}
              </button>
            ))}

            <div className="flex-1" />

            <button
              onClick={() => saveMut.mutate()}
              disabled={saveMut.isPending}
              className="flex items-center gap-1.5 text-xs px-3 py-1.5 rounded-lg bg-canvas-elevated border border-canvas-border hover:border-accent/50 text-slate-300 transition-colors disabled:opacity-40"
            >
              {saveMut.isPending ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <Save className="w-3.5 h-3.5" />}
              Save
            </button>
            <button
              onClick={() => runMut.mutate()}
              disabled={runMut.isPending}
              className="flex items-center gap-1.5 text-xs px-3 py-1.5 rounded-lg bg-success/15 border border-success/30 hover:bg-success/25 text-success transition-colors disabled:opacity-40"
            >
              {runMut.isPending ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <Play className="w-3.5 h-3.5" />}
              Run
            </button>
          </div>

          {/* Canvas + Inspector */}
          <div className="flex-1 flex overflow-hidden">
            <div className="flex-1">
              <ReactFlow
                nodes={nodes}
                edges={edges}
                onNodesChange={onNodesChange}
                onEdgesChange={onEdgesChange}
                onConnect={onConnect}
                nodeTypes={nodeTypes}
                onNodeClick={(_, node) => setSelectedNode(node)}
                onPaneClick={() => setSelectedNode(null)}
                fitView
                deleteKeyCode="Backspace"
              >
                <Background variant={BackgroundVariant.Dots} gap={20} size={1} color="#2a2f42" />
                <Controls />
                <MiniMap
                  nodeColor={() => '#6366f1'}
                  maskColor="rgba(13,15,20,0.7)"
                />
              </ReactFlow>
            </div>

            {selectedNode && (
              <Inspector
                node={selectedNode}
                onUpdate={updateNodeData}
                onClose={() => setSelectedNode(null)}
              />
            )}
          </div>
        </div>
      )}
    </div>
  )
}
