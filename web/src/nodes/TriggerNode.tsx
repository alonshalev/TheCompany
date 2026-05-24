import { memo } from 'react'
import { Handle, Position, type NodeProps } from '@xyflow/react'
import { Zap, Clock } from 'lucide-react'

export interface TriggerNodeData {
  label: string
  kind: 'cron' | 'webhook' | 'manual'
  cronExpr?: string
  webhookPath?: string
}

function TriggerNode({ data, selected }: NodeProps) {
  const d = data as TriggerNodeData
  const Icon = d.kind === 'cron' ? Clock : Zap

  return (
    <div
      className={`relative bg-canvas-elevated border-2 ${selected ? 'border-accent' : 'border-success/50'} rounded-xl px-4 py-3 w-44 shadow-lg`}
    >
      {/* Trigger nodes are always entry points — no input handle */}
      <div className="flex items-center gap-2 mb-1">
        <div className="bg-success/15 p-1.5 rounded-lg">
          <Icon className="w-3.5 h-3.5 text-success" />
        </div>
        <span className="text-xs font-medium text-white truncate">{d.label}</span>
      </div>

      {d.cronExpr && (
        <p className="text-xs font-mono text-muted truncate">{d.cronExpr}</p>
      )}
      {d.webhookPath && (
        <p className="text-xs font-mono text-muted truncate">/{d.webhookPath}</p>
      )}
      {d.kind === 'manual' && (
        <p className="text-xs text-muted">Manual trigger</p>
      )}

      <Handle type="source" position={Position.Bottom} className="!w-2 !h-2" />
    </div>
  )
}

export default memo(TriggerNode)
