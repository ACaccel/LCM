/**
 * ModuleBoundaryNode.tsx
 *
 * One of a module's own ports, drawn as a node.
 *
 * Rendering the boundary this way is what turns port binding into ordinary edge
 * drawing: an edge from input 0 to an inner node IS
 * `inPort: [{modulePort: 0, nodeName, nodePort}]`. It faces the graph, so a
 * module input is a source on the canvas and a module output is a target.
 */

import { Handle, Position, type NodeProps } from 'reactflow';
import type { CanvasNodeData } from '../model/types';
import '../styles/PolicyCanvas.css';

export default function ModuleBoundaryNode({ data, selected }: NodeProps<CanvasNodeData>) {
  const side = data.boundary?.side ?? 'in';
  const port = data.boundary?.port ?? 0;

  return (
    <div className={`module-boundary ${side}${selected ? ' selected' : ''}`}>
      {side === 'out' && <Handle type="target" position={Position.Left} id="tgt-0" />}
      <span className="mb-arrow">{side === 'in' ? '▸' : '▸'}</span>
      <span className="mb-label">
        {side === 'in' ? 'input' : 'output'} {port}
      </span>
      {side === 'in' && <Handle type="source" position={Position.Right} id="src-0" />}
    </div>
  );
}
