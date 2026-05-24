import { memo } from 'react'
import { Handle, Position, type NodeProps } from '@xyflow/react'
import { Bot } from 'lucide-react'

export interface AgentNodeData {
  label: string
  model?: string
  role?: string
  status?: 'idle' | 'running' | 'done' | 'error'
}

const statusRing: Record<string, string> = {
  idle:    'border-canvas-border',
  running: 'border-accent animate-pulse-slow',
  done:    'border-success',
  error:   'border-danger',
}

function AgentNode({ data, selected }: NodeProps) {
  const d = data as AgentNodeData
  const ring = statusRing[d.status ?? 'idle']

  return (
    <div
      className={`relative bg-canvas-elevated border-2 ${ring} ${selected ? 'border-accent' : ''} rounded-xl px-4 py-3 w-44 shadow-lg`}
    >
      <Handle type="target" position={Position.Top} className="!w-2 !h-2" />

      <div className="flex items-center gap-2 mb-1">
        <div className="bg-accent/15 p-1.5 rounded-lg">
          <Bot className="w-3.5 h-3.5 text-accent-hover" />
        </div>
        <span className="text-xs font-medium text-white truncate">{d.label}</span>
      </div>

      {d.role && (
        <p className="text-xs text-muted truncate">{d.role}</p>
      )}
      {d.model && (
        <p className="text-xs font-mono text-muted/70 truncate mt-0.5">{d.model}</p>
      )}

      <Handle type="source" position={Position.Bottom} className="!w-2 !h-2" />
    </div>
  )
}

export default memo(AgentNode)
