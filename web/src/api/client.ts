/**
 * Symbiont API client — typed fetch wrapper.
 *
 * All requests attach the Bearer token stored in sessionStorage.
 * All responses are JSON. Non-2xx responses throw an ApiError.
 *
 * Usage:
 *   import { api } from '@/api/client'
 *   const projects = await api.get<{ projects: Project[] }>('/v1/projects')
 */

const API_BASE = import.meta.env.VITE_API_BASE ?? ''

// ── Error type ─────────────────────────────────────────────────

export class ApiError extends Error {
  constructor(
    public readonly status: number,
    public readonly body: string,
  ) {
    super(`API ${status}: ${body}`)
    this.name = 'ApiError'
  }
}

// ── Domain types (mirror Go models) ──────────────────────────

export interface Organization {
  id: string
  name: string
  slug: string
  created_at: string
  updated_at: string
}

export interface Project {
  id: string
  org_id: string
  name: string
  slug: string
  description?: string
  budget_cents: number
  spend_cents: number
  settings: Record<string, unknown>
  created_at: string
  updated_at: string
  archived_at?: string
}

export type MemoryScope = 'private' | 'agent' | 'workflow' | 'project' | 'global'
export type MemoryType = 'working' | 'episodic' | 'semantic' | 'procedural'
export type TrustTier = 'verified' | 'observed' | 'untrusted'

export interface AgentSpec {
  id: string
  project_id: string
  name: string
  slug: string
  version: number
  role: string
  goal: string
  system_instructions: string
  provider_config_id?: string
  model: string
  memory_read_scopes: MemoryScope[]
  memory_write_scopes: MemoryScope[]
  budget_cents: number
  tool_grants: string[]
  guardrails: Record<string, unknown>
  synthesized_from?: string
  template_id?: string
  is_active: boolean
  created_by?: string
  created_at: string
  updated_at: string
}

export interface AgentInstance {
  id: string
  project_id: string
  spec_id: string
  name: string
  spend_cents: number
  is_active: boolean
  created_at: string
  updated_at: string
}

export type RunStatus = 'pending' | 'running' | 'paused' | 'succeeded' | 'failed' | 'cancelled'

export interface Run {
  id: string
  project_id: string
  workflow_id?: string
  agent_instance_id?: string
  status: RunStatus
  input: Record<string, unknown>
  output?: Record<string, unknown>
  error?: string
  spend_cents: number
  started_at?: string
  completed_at?: string
  created_at: string
  updated_at: string
}

export type EventKind =
  | 'run_started' | 'run_succeeded' | 'run_failed' | 'run_cancelled'
  | 'node_started' | 'node_succeeded' | 'node_failed'
  | 'agent_step' | 'model_call' | 'tool_call' | 'tool_result'
  | 'memory_write' | 'memory_read'
  | 'approval_requested' | 'approval_resolved'

export interface RunEvent {
  id: string
  run_id: string
  seq: number
  kind: EventKind
  node_id?: string
  agent_instance_id?: string
  payload: Record<string, unknown>
  created_at: string
}

export interface Workflow {
  id: string
  project_id: string
  name: string
  slug: string
  description?: string
  version: number
  definition: WorkflowDefinition
  is_active: boolean
  created_by?: string
  created_at: string
  updated_at: string
}

export interface WorkflowDefinition {
  nodes: WorkflowNode[]
  edges: WorkflowEdge[]
}

export interface WorkflowNode {
  id: string
  kind: string
  label: string
  config: Record<string, unknown>
  position: { x: number; y: number }
}

export interface WorkflowEdge {
  id: string
  source: string
  target: string
  condition?: string
}

export interface MemoryRecord {
  id: string
  project_id: string
  memory_type: MemoryType
  scope: MemoryScope
  content: string
  metadata: Record<string, unknown>
  trust_tier: TrustTier
  confidence: number
  is_quarantined: boolean
  access_count: number
  created_at: string
  updated_at: string
}

export interface MemoryEntity {
  id: string
  project_id: string
  entity_type: string
  name: string
  properties: Record<string, unknown>
  trust_tier: TrustTier
  created_at: string
  updated_at: string
}

export interface MemoryRelation {
  id: string
  project_id: string
  from_entity_id: string
  to_entity_id: string
  relation_type: string
  properties: Record<string, unknown>
  confidence: number
  created_at: string
}

export interface GraphResult {
  entities: MemoryEntity[]
  relations: MemoryRelation[]
}

// ── Auth helpers ──────────────────────────────────────────────

const API_KEY_STORAGE = 'symbiont_api_key'

export function getStoredApiKey(): string | null {
  return sessionStorage.getItem(API_KEY_STORAGE)
}

export function setStoredApiKey(key: string): void {
  sessionStorage.setItem(API_KEY_STORAGE, key)
}

export function clearStoredApiKey(): void {
  sessionStorage.removeItem(API_KEY_STORAGE)
}

// ── Core fetch wrapper ────────────────────────────────────────

async function request<T>(
  method: string,
  path: string,
  body?: unknown,
  extraHeaders?: Record<string, string>,
): Promise<T> {
  const apiKey = getStoredApiKey()
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    ...(apiKey ? { Authorization: `Bearer ${apiKey}` } : {}),
    ...extraHeaders,
  }

  const res = await fetch(`${API_BASE}${path}`, {
    method,
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })

  const text = await res.text()
  if (!res.ok) {
    throw new ApiError(res.status, text)
  }
  if (!text) return undefined as T
  return JSON.parse(text) as T
}

// ── Typed API surface ─────────────────────────────────────────

export const api = {
  // ── Health ──────────────────────────────────────────────────
  health: () => request<{ status: string }>('GET', '/healthz'),

  // ── Projects ────────────────────────────────────────────────
  listProjects: () =>
    request<{ projects: Project[] }>('GET', '/v1/projects'),
  createProject: (body: { name: string; slug: string; description?: string; budget_cents?: number }) =>
    request<Project>('POST', '/v1/projects', body),
  getProject: (projectID: string) =>
    request<Project>('GET', `/v1/projects/${projectID}`),
  updateProject: (projectID: string, body: Partial<Project>) =>
    request<Project>('PUT', `/v1/projects/${projectID}`, body),

  // ── Agent Specs ──────────────────────────────────────────────
  listAgentSpecs: (projectID: string) =>
    request<{ agents: AgentSpec[]; count: number }>('GET', `/v1/projects/${projectID}/agents`),
  createAgentSpec: (projectID: string, body: Partial<AgentSpec>) =>
    request<AgentSpec>('POST', `/v1/projects/${projectID}/agents`, body),
  synthesizeAgentSpec: (projectID: string, prompt: string, saveDirect = false) =>
    request<{ draft: AgentSpec; message: string } | AgentSpec>(
      'POST', `/v1/projects/${projectID}/agents/synthesize`,
      { prompt, save_direct: saveDirect },
    ),
  getAgentSpec: (projectID: string, specID: string) =>
    request<AgentSpec>('GET', `/v1/projects/${projectID}/agents/${specID}`),
  updateAgentSpec: (projectID: string, specID: string, body: Partial<AgentSpec>) =>
    request<AgentSpec>('PUT', `/v1/projects/${projectID}/agents/${specID}`, body),
  instantiateAgent: (projectID: string, specID: string, name?: string) =>
    request<AgentInstance>('POST', `/v1/projects/${projectID}/agents/${specID}/instantiate`, { name }),
  listTemplates: (projectID: string) =>
    request<{ templates: { id: string; description: string; spec: AgentSpec }[]; count: number }>(
      'GET', `/v1/projects/${projectID}/agents/templates`,
    ),

  // ── Workflows ────────────────────────────────────────────────
  listWorkflows: (projectID: string) =>
    request<{ workflows: Workflow[] }>('GET', `/v1/projects/${projectID}/workflows`),
  createWorkflow: (projectID: string, body: { name: string; slug: string; description?: string; definition: WorkflowDefinition }) =>
    request<Workflow>('POST', `/v1/projects/${projectID}/workflows`, body),
  getWorkflow: (projectID: string, workflowID: string) =>
    request<Workflow>('GET', `/v1/projects/${projectID}/workflows/${workflowID}`),
  updateWorkflow: (projectID: string, workflowID: string, body: Partial<Workflow>) =>
    request<Workflow>('PUT', `/v1/projects/${projectID}/workflows/${workflowID}`, body),
  startWorkflowRun: (projectID: string, workflowID: string, input?: Record<string, unknown>) =>
    request<Run>('POST', `/v1/projects/${projectID}/workflows/${workflowID}/run`, { input }),

  // ── Runs ─────────────────────────────────────────────────────
  listRuns: (projectID: string) =>
    request<{ runs: Run[] }>('GET', `/v1/projects/${projectID}/runs`),
  startRun: (projectID: string, body: { agent_instance_id: string; input?: Record<string, unknown> }) =>
    request<Run>('POST', `/v1/projects/${projectID}/runs`, body),
  getRun: (projectID: string, runID: string) =>
    request<Run>('GET', `/v1/projects/${projectID}/runs/${runID}`),
  getRunEvents: (projectID: string, runID: string) =>
    request<{ events: RunEvent[]; count: number }>('GET', `/v1/projects/${projectID}/runs/${runID}/events`),

  // ── Memory ───────────────────────────────────────────────────
  queryMemory: (projectID: string, params: { q?: string; type?: MemoryType; scope?: MemoryScope; min_trust?: TrustTier }) => {
    const qs = new URLSearchParams()
    if (params.q) qs.set('q', params.q)
    if (params.type) qs.set('type', params.type)
    if (params.scope) qs.set('scope', params.scope)
    if (params.min_trust) qs.set('min_trust', params.min_trust)
    return request<{ results: MemoryRecord[]; count: number }>('GET', `/v1/projects/${projectID}/memory?${qs}`)
  },
  writeMemory: (projectID: string, body: { content: string; type?: MemoryType; scope?: MemoryScope; trust_tier?: TrustTier; confidence?: number; metadata?: Record<string, unknown> }) =>
    request<MemoryRecord>('POST', `/v1/projects/${projectID}/memory`, body),
  getKnowledgeGraph: (projectID: string, params?: { entity_type?: string; relation_type?: string; depth?: number }) => {
    const qs = new URLSearchParams()
    if (params?.entity_type) qs.set('entity_type', params.entity_type)
    if (params?.relation_type) qs.set('relation_type', params.relation_type)
    if (params?.depth) qs.set('depth', String(params.depth))
    return request<GraphResult>('GET', `/v1/projects/${projectID}/memory/graph?${qs}`)
  },
}
