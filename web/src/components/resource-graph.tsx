import { Background, Controls, Handle, MarkerType, Position, ReactFlow, ReactFlowProvider } from "@xyflow/react";
import type { Edge, Node, NodeProps } from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { memo, useMemo } from "react";
import { healthView } from "../lib/status";
import type { RelationType, Topology } from "../lib/topology";
import { s } from "../strings";

interface ResourceNodeData extends Record<string, unknown> {
  name: string;
  typeLabel: string;
  tone: string;
  icon: string;
  healthLabel: string;
  missing: boolean;
  focus: boolean;
  onOpen: (name: string) => void;
}

type ResourceNode = Node<ResourceNodeData, "resource">;

/**
 * A resource as a button: keyboard users reach it with Tab and open it with
 * Enter. Health is written as text and an icon next to the coloured edge.
 */
const ResourceNodeView = memo(function ResourceNodeView({ data }: NodeProps<ResourceNode>) {
  const cls = `topo-node ${data.missing ? "neutral" : data.tone}${data.focus ? " focus" : ""}`;
  return (
    <div className={cls}>
      <Handle type="target" position={Position.Left} isConnectable={false} />
      <button type="button" className="topo-open nodrag" aria-label={data.missing ? `${data.name}，${s.topology.unresolved}` : `${s.topology.open(data.name)}，${data.typeLabel}，${data.healthLabel}`} disabled={data.missing} onClick={() => data.onOpen(data.name)}>
        <span className="tn-name" title={data.name}>
          {data.name}
        </span>
        <span className="tn-meta">
          <span>{data.missing ? s.topology.unresolved : data.typeLabel}</span>
          {!data.missing && (
            <span>
              <span aria-hidden="true">{data.icon} </span>
              {data.healthLabel}
            </span>
          )}
        </span>
      </button>
      <Handle type="source" position={Position.Right} isConnectable={false} />
    </div>
  );
});

const nodeTypes = { resource: ResourceNodeView };

const EDGE_STYLE: Record<RelationType, { stroke: string; dash?: string }> = {
  dependsOn: { stroke: "var(--muted-foreground)" },
  runsOn: { stroke: "var(--muted-foreground)", dash: "2 4" },
  controlPath: { stroke: "var(--info)", dash: "7 4" },
};

/** Legend of the three relation types, matching the edge styles. */
export function TopologyLegend() {
  return (
    <div className="topo-legend" aria-label={s.topology.legend}>
      {(Object.keys(EDGE_STYLE) as RelationType[]).map((t) => (
        <span key={t}>
          <i className={`edge-key ${t}`} aria-hidden="true" />
          {s.relations.types[t] ?? t}
        </span>
      ))}
    </div>
  );
}

interface Props {
  topology: Topology;
  onOpen: (name: string) => void;
  /** The resource whose page this graph is on, outlined. */
  focus?: string;
  /** A compact canvas for a tab. */
  small?: boolean;
}

function Inner({ topology, onOpen, focus, small }: Props) {
  const nodes = useMemo<ResourceNode[]>(
    () =>
      topology.nodes.map((n) => {
        const view = healthView(n.health);
        return {
          id: n.name,
          type: "resource",
          position: { x: n.x, y: n.y },
          draggable: false,
          connectable: false,
          data: {
            name: n.name,
            typeLabel: n.type === "unknown" ? "" : (s.resourceType[n.type] ?? n.type),
            tone: view.tone,
            icon: view.icon,
            healthLabel: view.label,
            missing: n.missing,
            focus: n.name === focus,
            onOpen,
          },
        };
      }),
    [topology.nodes, focus, onOpen],
  );
  const edges = useMemo<Edge[]>(
    () =>
      topology.edges.map((e) => {
        const style = EDGE_STYLE[e.type];
        return {
          id: e.id,
          source: e.from,
          target: e.to,
          type: "smoothstep",
          ariaLabel: `${e.from} ${s.relations.types[e.type] ?? e.type} ${e.to}`,
          style: { stroke: style.stroke, strokeWidth: 1.6, strokeDasharray: style.dash },
          markerEnd: { type: MarkerType.ArrowClosed, color: style.stroke },
        };
      }),
    [topology.edges],
  );
  return (
    <div className={`graph-wrap topo-wrap${small ? " small" : ""}`}>
      <ReactFlow
        nodes={nodes}
        edges={edges}
        nodeTypes={nodeTypes}
        nodesDraggable={false}
        nodesConnectable={false}
        elementsSelectable={false}
        fitView
        fitViewOptions={{ padding: 0.15, minZoom: small ? 0.5 : 0.45, maxZoom: 1 }}
        minZoom={0.2}
        maxZoom={1.5}
        colorMode="system"
        aria-label={s.topology.graph}
      >
        <Background gap={20} />
        <Controls showInteractive={false} />
      </ReactFlow>
    </div>
  );
}

/**
 * Resources and the relations between them. Shared by the topology page and
 * the relations tab; layout comes from lib/topology.
 */
export function ResourceGraph(props: Props) {
  return (
    <ReactFlowProvider>
      <Inner {...props} />
    </ReactFlowProvider>
  );
}
