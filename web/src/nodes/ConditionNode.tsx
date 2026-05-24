import { memo } from 'react'
import { Handle, Position, type NodeProps } from '@xyflow/react'
import { GitBranch } from 'lucide-react'

export interface ConditionNodeData {
  label: string
  expression?: string
}

function ConditionNode({ data, selected }: NodeProps) {
  const d = data as ConditionNodeData
  return (
    <div
      className={`relative bg-canvas-elevated border-2 ${selected ? 'border-accent' : 'border-warning/60'} rounded-xl px-4 py-3 w-44 shadow-lg`}
    >
      <Handle type="target" position={Position.Top} className="!w-2 !h-2" />

      <div className="flex items-center gap-2 mb-1">
        <div className="bg-warning/15 p-1.5 rounded-lg">
          <GitBranch className="w-3.5 h-3.5 text-warning" />
        </div>
        <span className="text-xs font-medium text-white truncate">{d.label}</span>
      </div>
      {d.expression && (
        <p className="text-xs font-mono text-muted truncate">{d.expression}</p>
      )}

      {/* Two output handles: true (left) and false (right) */}
      <Handle
        type="source"
        position={Position.Bottom}
        id="true"
        style={{ left: '30%' }}
        className="!w-2 !h-2"
      />
      <Handle
        type="source"
        position={Position.Bottom}
        id="false"
        style={{ left: '70%' }}
        className="!w-2 !h-2"
      />
      <div className="flex justify-between mt-1 px-1">
        <span className="text-xs text-success">true</span>
        <span className="text-xs text-danger">false</span>
      </div>
    </div>
  )
}

export default memo(ConditionNode)
