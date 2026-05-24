/**
 * TraceViewer — per-run event waterfall with live SSE tail.
 *
 * Route: /traces/:runID  (projectID supplied from parent shell)
 *
 * Layout
 * ──────
 * Left sidebar: run list for the project (click to switch)
 * Main: run header (status, timing, spend) + scrollable event waterfall
 * Live badge: pulses while the stream is connected
 *
 * The run list polls every 5 s so newly created runs appear automatically.
 * The event stream via useRunStream is enabled only for active runs
 * (pending / running / paused). Terminal runs load events via REST once.
 */

import { useEffect, useRef, useState } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import {
  CheckCircle2, XCircle, Clock, Loader2, Pause,
  ChevronRight, Zap, Bot, GitBranch, ShieldCheck,
  Database, Eye, AlertTriangle, Play, MemoryStick,
} from 'lucide-react'
import { api, type Run, type RunEvent, type EventKind } from '@/api/client'
import { useRunStream } from '@/hooks/useRunStream'

// ── Helpers ───────────────────────────────────────────────────

const TERMINAL_STATUSES = new Set(['succeeded', 'failed', 'cancelled'])

function isTerminal(status: string) {
  return TERMINAL_STATUSES.has(status)
}

function fmtDuration(startedAt?: string, finishedAt?: string): string {
  if (!startedAt) return '—'
  const start = new Date(startedAt).getTime()
  const end = finishedAt ? new Date(finishedAt).getTime() : Date.now()
  const ms = end - start
  if (ms < 1000) return `${ms}ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`
  return `${Math.floor(ms / 60_000)}m ${Math.round((ms % 60_000) / 1000)}s`
}

function fmtTs(iso: string): string {
  const d = new Date(iso)
  return d.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit', second: '2-digit', fractionalSecondDigits: 3 })
}

function centsToUSD(cents: number): string {
  return `$${(cents / 100).toFixed(4)}`
}

// ── Status badge ──────────────────────────────────────────────

const statusConfig: Record<string, { icon: React.FC<{ className?: string }>; color: string; label: string }> = {
  pending:   { icon: Clock,        color: 'text-muted',   label: 'Pending' },
  running:   { icon: Loader2,      color: 'text-accent',  label: 'Running' },
  paused:    { icon: Pause,        color: 'text-warning', label: 'Paused' },
  succeeded: { icon: CheckCircle2, color: 'text-success', label: 'Succeeded' },
  failed:    { icon: XCircle,      color: 'text-danger',  label: 'Failed' },
  cancelled: { icon: XCircle,      color: 'text-muted',   label: 'Cancelled' },
}

function RunStatusBadge({ status }: { status: string }) {
  const cfg = statusConfig[status] ?? statusConfig.pending
  const Icon = cfg.icon
  return (
    <span className={`flex items-center gap-1 text-xs font-medium ${cfg.color}`}>
      <Icon className={`w-3.5 h-3.5 ${status === 'running' ? 'animate-spin' : ''}`} />
      {cfg.label}
    </span>
  )
}

// ── Event kind metadata ───────────────────────────────────────

const eventMeta: Record<EventKind | string, { icon: React.FC<{ className?: string }>; color: string; label: string }> = {
  run_started:        { icon: Play,        color: 'text-accent',  label: 'Run started' },
  run_succeeded:      { icon: CheckCircle2,color: 'text-success', label: 'Run succeeded' },
  run_failed:         { icon: XCircle,     color: 'text-danger',  label: 'Run failed' },
  run_cancelled:      { icon: XCircle,     color: 'text-muted',   label: 'Run cancelled' },
  node_started:       { icon: ChevronRight,color: 'text-slate-400',label: 'Node started' },
  node_succeeded:     { icon: CheckCircle2,color: 'text-success', label: 'Node succeeded' },
  node_failed:        { icon: XCircle,     color: 'text-danger',  label: 'Node failed' },
  agent_step:         { icon: Bot,         color: 'text-accent',  label: 'Agent step' },
  model_call:         { icon: Zap,         color: 'text-accent-hover', label: 'Model call' },
  tool_call:          { icon: GitBranch,   color: 'text-warning', label: 'Tool call' },
  tool_result:        { icon: CheckCircle2,color: 'text-warning', label: 'Tool result' },
  memory_write:       { icon: Database,    color: 'text-purple-400', label: 'Memory write' },
  memory_read:        { icon: Eye,         color: 'text-purple-400', label: 'Memory read' },
  approval_requested: { icon: ShieldCheck, color: 'text-orange-400', label: 'Approval requested' },
  approval_resolved:  { icon: ShieldCheck, color: 'text-success',  label: 'Approval resolved' },
}

function getEventMeta(kind: string) {
  return eventMeta[kind] ?? { icon: AlertTriangle, color: 'text-muted', label: kind }
}

// ── Single event row ──────────────────────────────────────────

function EventRow({ event, index }: { event: RunEvent; index: number }) {
  const [expanded, setExpanded] = useState(false)
  const meta = getEventMeta(event.kind)
  const Icon = meta.icon

  const hasPayload = event.payload && Object.keys(event.payload).length > 0
  const payloadStr = hasPayload ? JSON.stringify(event.payload, null, 2) : null

  return (
    <div className="group">
      <button
        onClick={() => hasPayload && setExpanded(e => !e)}
        className={`w-full flex items-start gap-3 px-4 py-2.5 text-left transition-colors ${hasPayload ? 'hover:bg-canvas-elevated/60 cursor-pointer' : 'cursor-default'} ${index % 2 === 0 ? '' : 'bg-canvas-surface/30'}`}
      >
        {/* Timeline connector */}
        <div className="flex flex-col items-center mt-0.5 flex-shrink-0">
          <div className={`w-6 h-6 rounded-full bg-canvas-elevated border border-canvas-border flex items-center justify-center`}>
            <Icon className={`w-3 h-3 ${meta.color}`} />
          </div>
        </div>

        {/* Content */}
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2">
            <span className={`text-xs font-medium ${meta.color}`}>{meta.label}</span>
            {event.node_id && (
              <span className="text-xs text-muted font-mono truncate">{event.node_id}</span>
            )}
            <span className="ml-auto text-xs text-muted font-mono flex-shrink-0">{fmtTs(event.created_at)}</span>
          </div>

          {/* Quick payload preview */}
          {hasPayload && !expanded && (
            <p className="text-xs text-muted/80 mt-0.5 truncate font-mono">
              {event.payload.content
                ? String(event.payload.content).slice(0, 120)
                : event.payload.model
                  ? `model=${event.payload.model}`
                  : event.payload.tool_name
                    ? `tool=${event.payload.tool_name}`
                    : JSON.stringify(event.payload).slice(0, 100)}
            </p>
          )}
        </div>

        <span className="text-xs text-muted/50 font-mono flex-shrink-0 ml-2">#{event.seq}</span>
      </button>

      {/* Expanded payload */}
      {expanded && payloadStr && (
        <div className="mx-4 mb-2 bg-canvas rounded-lg border border-canvas-border overflow-x-auto">
          <pre className="px-4 py-3 text-xs font-mono text-slate-300 leading-relaxed whitespace-pre-wrap break-words">
            {payloadStr}
          </pre>
        </div>
      )}
    </div>
  )
}

// ── Run list sidebar ──────────────────────────────────────────

function RunList({
  projectID,
  activeID,
  onSelect,
}: {
  projectID: string
  activeID: string | null
  onSelect: (run: Run) => void
}) {
  const { data } = useQuery({
    queryKey: ['runs', projectID],
    queryFn: () => api.listRuns(projectID),
    refetchInterval: 5_000,
  })
  const runs = data?.runs ?? []

  return (
    <div className="w-52 flex-shrink-0 border-r border-canvas-border flex flex-col">
      <div className="px-3 py-3 border-b border-canvas-border">
        <span className="text-xs font-medium text-muted uppercase tracking-wider">Runs</span>
      </div>
      <div className="flex-1 overflow-y-auto py-1">
        {runs.length === 0 && (
          <p className="px-3 py-4 text-xs text-muted text-center">No runs yet</p>
        )}
        {runs.map(run => {
          const cfg = statusConfig[run.status] ?? statusConfig.pending
          const Icon = cfg.icon
          return (
            <button
              key={run.id}
              onClick={() => onSelect(run)}
              className={`w-full text-left flex items-center gap-2 px-3 py-2 text-xs transition-colors ${run.id === activeID ? 'text-accent-hover bg-accent/10' : 'text-slate-400 hover:text-slate-200 hover:bg-canvas-elevated'}`}
            >
              <Icon className={`w-3 h-3 flex-shrink-0 ${cfg.color} ${run.status === 'running' ? 'animate-spin' : ''}`} />
              <span className="truncate font-mono">{run.id.slice(0, 8)}</span>
              <span className="ml-auto text-muted/60 font-mono">{fmtDuration(run.started_at, run.completed_at)}</span>
            </button>
          )
        })}
      </div>
    </div>
  )
}

// ── Main page ─────────────────────────────────────────────────

export default function TraceViewer({ projectID }: { projectID: string | null }) {
  const { runID: routeRunID } = useParams()
  const navigate = useNavigate()

  const [activeRunID, setActiveRunID] = useState<string | null>(routeRunID ?? null)

  // Keep route in sync
  useEffect(() => {
    if (activeRunID) navigate(`/traces/${activeRunID}`, { replace: true })
  }, [activeRunID, navigate])

  // Fetch run metadata (poll for active runs)
  const runQ = useQuery({
    queryKey: ['run', projectID, activeRunID],
    queryFn: () => api.getRun(projectID!, activeRunID!),
    enabled: !!projectID && !!activeRunID,
    refetchInterval: (query) => {
      const data = query.state.data
      return data && isTerminal(data.status) ? false : 3_000
    },
  })
  const run = runQ.data

  // Live stream for active runs
  const streamEnabled = !!run && !isTerminal(run.status)
  const { events: streamEvents, status: streamStatus } = useRunStream(
    projectID,
    activeRunID,
    streamEnabled,
  )

  // REST events for terminal runs
  const restEventsQ = useQuery({
    queryKey: ['run-events', projectID, activeRunID],
    queryFn: () => api.getRunEvents(projectID!, activeRunID!),
    enabled: !!projectID && !!activeRunID && !!run && isTerminal(run.status),
  })

  const events: RunEvent[] = streamEnabled
    ? streamEvents
    : (restEventsQ.data?.events ?? [])

  // Auto-scroll to bottom as new events arrive
  const bottomRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (streamEnabled) {
      bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
    }
  }, [events.length, streamEnabled])

  if (!projectID) {
    return (
      <div className="flex-1 flex items-center justify-center text-muted text-sm">
        Select a project to view traces.
      </div>
    )
  }

  return (
    <div className="flex-1 flex overflow-hidden">
      {/* Run list */}
      <RunList
        projectID={projectID}
        activeID={activeRunID}
        onSelect={r => setActiveRunID(r.id)}
      />

      {/* Main content */}
      {!activeRunID ? (
        <div className="flex-1 flex items-center justify-center text-muted text-sm">
          Select a run to inspect.
        </div>
      ) : (
        <div className="flex-1 flex flex-col overflow-hidden">
          {/* Run header */}
          <div className="flex items-center gap-4 px-5 py-3 border-b border-canvas-border bg-canvas-surface flex-shrink-0">
            {run ? (
              <>
                <RunStatusBadge status={run.status} />
                <span className="text-xs font-mono text-muted">{run.id}</span>
                <div className="flex items-center gap-1 text-xs text-muted">
                  <Clock className="w-3 h-3" />
                  {fmtDuration(run.started_at, run.completed_at)}
                </div>
                <div className="flex items-center gap-1 text-xs text-muted">
                  <MemoryStick className="w-3 h-3" />
                  {centsToUSD(run.spend_cents)}
                </div>
                {run.error && (
                  <span className="text-xs text-danger truncate max-w-xs">{run.error}</span>
                )}
                <div className="flex-1" />
                {/* Live stream indicator */}
                {streamEnabled && (
                  <div className="flex items-center gap-1.5">
                    <span
                      className={`w-2 h-2 rounded-full ${streamStatus === 'connected' ? 'bg-success animate-pulse' : streamStatus === 'connecting' ? 'bg-warning animate-pulse' : 'bg-muted'}`}
                    />
                    <span className="text-xs text-muted">
                      {streamStatus === 'connected' ? 'Live' : streamStatus === 'connecting' ? 'Connecting…' : 'Stream'}
                    </span>
                  </div>
                )}
                <span className="text-xs text-muted">
                  {events.length} event{events.length !== 1 ? 's' : ''}
                </span>
              </>
            ) : runQ.isLoading ? (
              <Loader2 className="w-4 h-4 text-muted animate-spin" />
            ) : (
              <span className="text-xs text-danger">Run not found</span>
            )}
          </div>

          {/* Event waterfall */}
          <div className="flex-1 overflow-y-auto">
            {events.length === 0 ? (
              <div className="flex flex-col items-center justify-center h-full gap-3 text-muted">
                {streamStatus === 'connecting' ? (
                  <>
                    <Loader2 className="w-6 h-6 animate-spin" />
                    <span className="text-sm">Connecting to stream…</span>
                  </>
                ) : (
                  <>
                    <Clock className="w-6 h-6" />
                    <span className="text-sm">No events yet</span>
                  </>
                )}
              </div>
            ) : (
              <div className="divide-y divide-canvas-border/40">
                {events.map((evt, i) => (
                  <EventRow key={evt.id} event={evt} index={i} />
                ))}
                <div ref={bottomRef} />
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  )
}
