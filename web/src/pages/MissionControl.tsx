import { useQuery } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { Play, Clock, CheckCircle, XCircle, PauseCircle, Loader2, RefreshCw } from 'lucide-react'
import { api, type Run, type RunStatus, type Project } from '@/api/client'

// ── Helpers ───────────────────────────────────────────────────

function statusIcon(status: RunStatus) {
  switch (status) {
    case 'running':  return <Loader2 className="w-3.5 h-3.5 text-accent animate-spin" />
    case 'succeeded': return <CheckCircle className="w-3.5 h-3.5 text-success" />
    case 'failed':   return <XCircle className="w-3.5 h-3.5 text-danger" />
    case 'cancelled': return <XCircle className="w-3.5 h-3.5 text-muted" />
    case 'paused':   return <PauseCircle className="w-3.5 h-3.5 text-warning" />
    default:         return <Clock className="w-3.5 h-3.5 text-muted" />
  }
}

function statusBadge(status: RunStatus) {
  const base = 'inline-flex items-center gap-1 text-xs px-2 py-0.5 rounded-full font-medium'
  switch (status) {
    case 'running':   return `${base} bg-accent/15 text-accent-hover`
    case 'succeeded': return `${base} bg-success/15 text-success`
    case 'failed':    return `${base} bg-danger/15 text-danger`
    case 'paused':    return `${base} bg-warning/15 text-warning`
    default:          return `${base} bg-canvas-border text-muted`
  }
}

function centsToDollars(cents: number) {
  return `$${(cents / 100).toFixed(2)}`
}

function elapsed(run: Run) {
  if (!run.started_at) return '—'
  const start = new Date(run.started_at).getTime()
  const end = run.completed_at ? new Date(run.completed_at).getTime() : Date.now()
  const secs = Math.floor((end - start) / 1000)
  if (secs < 60) return `${secs}s`
  return `${Math.floor(secs / 60)}m ${secs % 60}s`
}

function relativeTime(iso: string) {
  const diff = Date.now() - new Date(iso).getTime()
  const mins = Math.floor(diff / 60_000)
  if (mins < 1) return 'just now'
  if (mins < 60) return `${mins}m ago`
  const hrs = Math.floor(mins / 60)
  if (hrs < 24) return `${hrs}h ago`
  return `${Math.floor(hrs / 24)}d ago`
}

// ── Spend gauge ───────────────────────────────────────────────

function SpendGauge({ label, spent, budget }: { label: string; spent: number; budget: number }) {
  const pct = budget > 0 ? Math.min((spent / budget) * 100, 100) : 0
  const color = pct > 90 ? 'bg-danger' : pct > 70 ? 'bg-warning' : 'bg-accent'

  return (
    <div className="bg-canvas-elevated border border-canvas-border rounded-lg p-4">
      <div className="flex justify-between text-xs text-muted mb-2">
        <span>{label}</span>
        <span>{centsToDollars(spent)} / {centsToDollars(budget)}</span>
      </div>
      <div className="h-1.5 bg-canvas-border rounded-full overflow-hidden">
        <div className={`h-full rounded-full transition-all ${color}`} style={{ width: `${pct}%` }} />
      </div>
    </div>
  )
}

// ── KPI card ──────────────────────────────────────────────────

function KpiCard({ label, value, sub }: { label: string; value: string | number; sub?: string }) {
  return (
    <div className="bg-canvas-elevated border border-canvas-border rounded-lg p-4">
      <p className="text-xs text-muted mb-1">{label}</p>
      <p className="text-2xl font-semibold text-white">{value}</p>
      {sub && <p className="text-xs text-muted mt-1">{sub}</p>}
    </div>
  )
}

// ── Main page ─────────────────────────────────────────────────

export default function MissionControl({ projectID }: { projectID: string | null }) {
  const navigate = useNavigate()

  const projectsQ = useQuery({
    queryKey: ['projects'],
    queryFn: () => api.listProjects(),
  })

  const runsQ = useQuery({
    queryKey: ['runs', projectID],
    queryFn: () => api.listRuns(projectID!),
    enabled: !!projectID,
    refetchInterval: 5_000, // poll every 5 s for live status
  })

  const projectQ = useQuery({
    queryKey: ['project', projectID],
    queryFn: () => api.getProject(projectID!),
    enabled: !!projectID,
  })

  if (!projectID) {
    return (
      <div className="flex-1 flex items-center justify-center text-muted text-sm">
        Select a project from the sidebar to get started.
      </div>
    )
  }

  const runs: Run[] = runsQ.data?.runs ?? []
  const project: Project | undefined = projectQ.data

  const activeRuns = runs.filter(r => r.status === 'running' || r.status === 'paused')
  const recentRuns = runs.slice(0, 20)
  const successRate = runs.length > 0
    ? Math.round((runs.filter(r => r.status === 'succeeded').length / runs.length) * 100)
    : 0
  const totalSpend = runs.reduce((acc, r) => acc + r.spend_cents, 0)

  return (
    <div className="flex-1 overflow-y-auto">
      {/* Header */}
      <div className="sticky top-0 bg-canvas-surface/95 backdrop-blur border-b border-canvas-border px-6 py-3 flex items-center justify-between z-10">
        <div>
          <h1 className="text-base font-semibold text-white">Mission Control</h1>
          {project && <p className="text-xs text-muted">{project.name}</p>}
        </div>
        <button
          onClick={() => runsQ.refetch()}
          className="flex items-center gap-1.5 text-xs text-muted hover:text-slate-200 transition-colors"
        >
          <RefreshCw className={`w-3.5 h-3.5 ${runsQ.isFetching ? 'animate-spin' : ''}`} />
          Refresh
        </button>
      </div>

      <div className="p-6 space-y-6">

        {/* KPI row */}
        <div className="grid grid-cols-2 lg:grid-cols-4 gap-3">
          <KpiCard label="Active runs" value={activeRuns.length} />
          <KpiCard label="Total runs" value={runs.length} />
          <KpiCard label="Success rate" value={`${successRate}%`} />
          <KpiCard label="Total spend" value={centsToDollars(totalSpend)} />
        </div>

        {/* Budget gauges */}
        {project && project.budget_cents > 0 && (
          <section>
            <h2 className="text-xs font-medium text-muted uppercase tracking-wider mb-3">Budget</h2>
            <div className="grid grid-cols-1 lg:grid-cols-2 gap-3">
              <SpendGauge
                label="Project budget"
                spent={project.spend_cents}
                budget={project.budget_cents}
              />
            </div>
          </section>
        )}

        {/* Active runs */}
        {activeRuns.length > 0 && (
          <section>
            <h2 className="text-xs font-medium text-muted uppercase tracking-wider mb-3">
              Active ({activeRuns.length})
            </h2>
            <div className="space-y-2">
              {activeRuns.map(run => (
                <div
                  key={run.id}
                  onClick={() => navigate(`/traces/${run.id}`)}
                  className="flex items-center justify-between bg-canvas-elevated border border-accent/30 rounded-lg px-4 py-3 cursor-pointer hover:border-accent/60 transition-colors"
                >
                  <div className="flex items-center gap-3 min-w-0">
                    {statusIcon(run.status)}
                    <div className="min-w-0">
                      <p className="text-sm text-slate-200 font-mono truncate">{run.id.slice(0, 8)}…</p>
                      <p className="text-xs text-muted">{relativeTime(run.created_at)}</p>
                    </div>
                  </div>
                  <div className="flex items-center gap-4 flex-shrink-0">
                    <span className="text-xs text-muted">{elapsed(run)}</span>
                    <span className="text-xs text-muted">{centsToDollars(run.spend_cents)}</span>
                    <span className={statusBadge(run.status)}>{run.status}</span>
                  </div>
                </div>
              ))}
            </div>
          </section>
        )}

        {/* Recent runs table */}
        <section>
          <h2 className="text-xs font-medium text-muted uppercase tracking-wider mb-3">
            Recent runs
          </h2>
          {runsQ.isLoading ? (
            <div className="flex items-center gap-2 text-muted text-sm py-8 justify-center">
              <Loader2 className="w-4 h-4 animate-spin" /> Loading…
            </div>
          ) : recentRuns.length === 0 ? (
            <div className="text-center py-12 text-muted text-sm">
              <Play className="w-8 h-8 mx-auto mb-2 opacity-30" />
              <p>No runs yet. Start one from the Workflows page.</p>
            </div>
          ) : (
            <div className="bg-canvas-elevated border border-canvas-border rounded-lg overflow-hidden">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b border-canvas-border">
                    <th className="text-left px-4 py-2.5 text-xs font-medium text-muted">Run ID</th>
                    <th className="text-left px-4 py-2.5 text-xs font-medium text-muted">Status</th>
                    <th className="text-left px-4 py-2.5 text-xs font-medium text-muted">Duration</th>
                    <th className="text-left px-4 py-2.5 text-xs font-medium text-muted">Spend</th>
                    <th className="text-left px-4 py-2.5 text-xs font-medium text-muted">Started</th>
                  </tr>
                </thead>
                <tbody>
                  {recentRuns.map((run, i) => (
                    <tr
                      key={run.id}
                      onClick={() => navigate(`/traces/${run.id}`)}
                      className={`cursor-pointer hover:bg-canvas-border/30 transition-colors ${i < recentRuns.length - 1 ? 'border-b border-canvas-border' : ''}`}
                    >
                      <td className="px-4 py-3 font-mono text-xs text-slate-400">{run.id.slice(0, 8)}…</td>
                      <td className="px-4 py-3">
                        <span className={statusBadge(run.status)}>
                          {statusIcon(run.status)}
                          {run.status}
                        </span>
                      </td>
                      <td className="px-4 py-3 text-xs text-muted">{elapsed(run)}</td>
                      <td className="px-4 py-3 text-xs text-muted">{centsToDollars(run.spend_cents)}</td>
                      <td className="px-4 py-3 text-xs text-muted">{relativeTime(run.created_at)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </section>
      </div>
    </div>
  )
}
