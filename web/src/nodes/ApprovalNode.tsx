import { memo } from 'react'
import { Handle, Position, type NodeProps } from '@xyflow/react'
import { ShieldCheck } from 'lucide-react'

export interface ApprovalNodeData {
  label: string
  status?: 'pending' | 'approved' | 'rejected'
  timeoutHours?: number
}

const statusColor: Record<string, string> = {
  pending:  'text-warning',
  approved: 'text-success',
  rejected: 'text-danger',
}

function ApprovalNode({ data, selected }: NodeProps) {
  const d = data as ApprovalNodeData
  const color = statusColor[d.status ?? 'pending']

  return (
    <div
      className={`relative bg-canvas-elevated border-2 ${selected ? 'border-accent' : 'border-muted/50'} rounded-xl px-4 py-3 w-44 shadow-lg`}
    >
      <Handle type="target" position={Position.Top} className="!w-2 !h-2" />

      <div className="flex items-center gap-2 mb-1">
        <div className="bg-muted/15 p-1.5 rounded-lg">
          <ShieldCheck className="w-3.5 h-3.5 text-muted" />
        </div>
        <span className="text-xs font-medium text-white truncate">{d.label}</span>
      </div>

      <div className="flex items-center justify-between">
        {d.status && (
          <span className={`text-xs font-medium ${color}`}>{d.status}</span>
        )}
        {d.timeoutHours && (
          <span className="text-xs text-muted">{d.timeoutHours}h timeout</span>
        )}
      </div>

      <Handle type="source" position={Position.Bottom} className="!w-2 !h-2" />
    </div>
  )
}

export default memo(ApprovalNode)
