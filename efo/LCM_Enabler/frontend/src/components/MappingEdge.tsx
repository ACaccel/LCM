/**
 * MappingEdge.tsx
 *
 * Custom edge that shows, on the canvas, whether a connection is configured.
 *
 * Edges used to be unlabelled, so a pass-through edge and a broken one looked
 * identical and the only way to find out was to deploy and fire. The status is
 * computed in PolicyCanvas's derived edge array and passed in as `_status`, so
 * it never reaches the saved policy.
 */

import { BaseEdge, EdgeLabelRenderer, getBezierPath, type EdgeProps } from 'reactflow';
import type { EdgeStatus, MappingEdgeData } from '../model/edgeStatus';

const CHIP_COLOR: Record<EdgeStatus['kind'], string> = {
  passthrough: '#556070',
  mapped: '#4a90d9',
  invalid: '#e07b39',
};

export default function MappingEdge(props: EdgeProps<MappingEdgeData>): JSX.Element {
  const {
    id,
    sourceX,
    sourceY,
    targetX,
    targetY,
    sourcePosition,
    targetPosition,
    style,
    markerEnd,
    data,
  } = props;

  const [path, labelX, labelY] = getBezierPath({
    sourceX,
    sourceY,
    sourcePosition,
    targetX,
    targetY,
    targetPosition,
  });

  const status = data?._status;

  return (
    <>
      {/* style carries the run highlighting, so it must be forwarded. */}
      <BaseEdge id={id} path={path} style={style} markerEnd={markerEnd} />
      {status && (
        <EdgeLabelRenderer>
          <div
            className="mapping-edge-chip"
            style={{
              transform: `translate(-50%, -50%) translate(${labelX}px, ${labelY}px)`,
              borderColor: CHIP_COLOR[status.kind],
              color: CHIP_COLOR[status.kind],
            }}
            title={status.detail}
          >
            {status.label}
          </div>
        </EdgeLabelRenderer>
      )}
    </>
  );
}
