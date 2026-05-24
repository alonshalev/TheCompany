import { useState } from 'react'
import { BrowserRouter, Routes, Route, NavLink, Navigate } from 'react-router-dom'
import {
  LayoutDashboard, GitBranch, Brain, ActivitySquare,
  Settings, LogOut, ChevronDown,
} from 'lucide-react'
import { useQuery } from '@tanstack/react-query'
import { api, ApiError } from '@/api/client'
import { useAPIKey } from '@/hooks/useAPIKey'
import MissionControl from '@/pages/MissionControl'
import WorkflowBuilder from '@/pages/WorkflowBuilder'
import MemoryExplorer from '@/pages/MemoryExplorer'
import TraceViewer from '@/pages/TraceViewer'

// ── Login gate ────────────────────────────────────────────────

function LoginPage({ onLogin }: { onLogin: (key: string) => void }) {
  const [key, setKey] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    setError('')
    setLoading(true)
    try {
      // Temporarily set key so the api client can use it
      sessionStorage.setItem('symbiont_api_key', key.trim())
      await api.listProjects()
      onLogin(key.trim())
    } catch (err) {
      sessionStorage.removeItem('symbiont_api_key')
      if (err instanceof ApiError && err.status === 401) {
        setError('Invalid API key.')
      } else {
        setError('Could not reach API. Is the server running?')
      }
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="min-h-screen bg-canvas flex items-center justify-center p-4">
      <div className="w-full max-w-sm">
        <div className="mb-8 text-center">
          <div className="text-2xl font-semibold tracking-tight text-white">⬡ Symbiont</div>
          <p className="text-sm text-muted mt-1">Enter your API key to continue</p>
        </div>
        <form onSubmit={handleSubmit} className="space-y-4">
          <div>
            <label className="block text-xs font-medium text-slate-400 mb-1">API Key</label>
            <input
              type="password"
              value={key}
              onChange={e => setKey(e.target.value)}
              placeholder="sym_..."
              className="w-full bg-canvas-elevated border border-canvas-border rounded-lg px-3 py-2 text-sm text-slate-100 placeholder-muted focus:outline-none focus:border-accent focus:ring-1 focus:ring-accent font-mono"
              autoFocus
            />
          </div>
          {error && (
            <p className="text-xs text-danger">{error}</p>
          )}
          <button
            type="submit"
            disabled={!key.trim() || loading}
            className="w-full bg-accent hover:bg-accent-hover disabled:opacity-40 disabled:cursor-not-allowed text-white text-sm font-medium py-2 rounded-lg transition-colors"
          >
            {loading ? 'Connecting…' : 'Connect'}
          </button>
        </form>
      </div>
    </div>
  )
}

// ── Project selector ──────────────────────────────────────────

function ProjectSelector({
  projectID,
  onSelect,
}: {
  projectID: string | null
  onSelect: (id: string) => void
}) {
  const [open, setOpen] = useState(false)
  const { data } = useQuery({
    queryKey: ['projects'],
    queryFn: () => api.listProjects(),
  })

  const projects = data?.projects ?? []
  const current = projects.find(p => p.id === projectID)

  return (
    <div className="relative">
      <button
        onClick={() => setOpen(o => !o)}
        className="w-full flex items-center justify-between px-3 py-2 text-sm bg-canvas-elevated border border-canvas-border rounded-lg hover:border-accent/50 transition-colors"
      >
        <span className="truncate text-slate-300">
          {current?.name ?? 'Select project…'}
        </span>
        <ChevronDown className="w-3.5 h-3.5 text-muted flex-shrink-0 ml-2" />
      </button>
      {open && (
        <div className="absolute top-full mt-1 w-full bg-canvas-elevated border border-canvas-border rounded-lg shadow-xl z-50 py-1 animate-fade-in">
          {projects.length === 0 && (
            <p className="px-3 py-2 text-xs text-muted">No projects yet</p>
          )}
          {projects.map(p => (
            <button
              key={p.id}
              onClick={() => { onSelect(p.id); setOpen(false) }}
              className={`w-full text-left px-3 py-2 text-sm hover:bg-canvas-border/50 transition-colors ${p.id === projectID ? 'text-accent-hover' : 'text-slate-300'}`}
            >
              {p.name}
            </button>
          ))}
        </div>
      )}
    </div>
  )
}

// ── Nav item ──────────────────────────────────────────────────

function NavItem({ to, icon: Icon, label }: { to: string; icon: React.ElementType; label: string }) {
  return (
    <NavLink
      to={to}
      className={({ isActive }) =>
        `flex items-center gap-2.5 px-3 py-2 rounded-lg text-sm transition-colors ${
          isActive
            ? 'bg-accent/15 text-accent-hover'
            : 'text-slate-400 hover:text-slate-200 hover:bg-canvas-elevated'
        }`
      }
    >
      <Icon className="w-4 h-4 flex-shrink-0" />
      {label}
    </NavLink>
  )
}

// ── Main app shell ────────────────────────────────────────────

function AppShell() {
  const { clearApiKey } = useAPIKey()
  const [projectID, setProjectID] = useState<string | null>(null)

  return (
    <div className="flex h-full">
      {/* Sidebar */}
      <aside className="w-52 flex-shrink-0 bg-canvas-surface border-r border-canvas-border flex flex-col">
        {/* Logo */}
        <div className="px-4 py-4 border-b border-canvas-border">
          <div className="text-base font-semibold tracking-tight text-white">⬡ Symbiont</div>
        </div>

        {/* Project selector */}
        <div className="px-3 py-3 border-b border-canvas-border">
          <ProjectSelector projectID={projectID} onSelect={setProjectID} />
        </div>

        {/* Nav links */}
        <nav className="flex-1 px-3 py-3 space-y-0.5">
          <NavItem to="/mission" icon={LayoutDashboard} label="Mission Control" />
          <NavItem to="/workflows" icon={GitBranch} label="Workflows" />
          <NavItem to="/memory" icon={Brain} label="Memory" />
          <NavItem to="/traces" icon={ActivitySquare} label="Traces" />
        </nav>

        {/* Footer */}
        <div className="px-3 py-3 border-t border-canvas-border space-y-0.5">
          <NavItem to="/settings" icon={Settings} label="Settings" />
          <button
            onClick={clearApiKey}
            className="w-full flex items-center gap-2.5 px-3 py-2 rounded-lg text-sm text-slate-400 hover:text-slate-200 hover:bg-canvas-elevated transition-colors"
          >
            <LogOut className="w-4 h-4" />
            Sign out
          </button>
        </div>
      </aside>

      {/* Main content */}
      <main className="flex-1 overflow-hidden flex flex-col">
        <Routes>
          <Route path="/" element={<Navigate to="/mission" replace />} />
          <Route path="/mission" element={<MissionControl projectID={projectID} />} />
          <Route path="/workflows" element={<WorkflowBuilder projectID={projectID} />} />
          <Route path="/workflows/:workflowID" element={<WorkflowBuilder projectID={projectID} />} />
          <Route path="/memory" element={<MemoryExplorer projectID={projectID} />} />
          <Route path="/traces" element={<TraceViewer projectID={projectID} runID={null} />} />
          <Route path="/traces/:runID" element={<TraceViewer projectID={projectID} runID={null} />} />
          <Route path="/settings" element={<SettingsPage />} />
        </Routes>
      </main>
    </div>
  )
}

// ── Settings stub ─────────────────────────────────────────────

function SettingsPage() {
  return (
    <div className="p-8">
      <h1 className="text-lg font-semibold text-white mb-2">Settings</h1>
      <p className="text-sm text-muted">Settings — coming in Part 6 (Governance).</p>
    </div>
  )
}

// ── Root component ────────────────────────────────────────────

export default function App() {
  const { apiKey, setApiKey } = useAPIKey()

  if (!apiKey) {
    return <LoginPage onLogin={setApiKey} />
  }

  return (
    <BrowserRouter>
      <AppShell />
    </BrowserRouter>
  )
}
