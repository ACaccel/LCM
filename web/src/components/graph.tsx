import {
  Background,
  Controls,
  Handle,
  MarkerType,
  MiniMap,
  Position as HandlePosition,
  ReactFlow,
  ReactFlowProvider,
  useReactFlow,
} from "@xyflow/react";
import { applyNodeChanges } from "@xyflow/react";
import type { Connection, Edge, Node, NodeChange, NodeProps } from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { memo, useCallback, useMemo, useState } from "react";
import type { DragEvent } from "react";
import type { NodeStatus, NodeType } from "../api/types";
import { nodeStatusView } from "../lib/status";
import type { MappedIssue } from "../policy/issues";
import type { ModelEdge, ModelNode, PolicyModel, Position } from "../policy/model";
import { categoryColor, hasInputPort, portsOf } from "../policy/schema";
import { s } from "../strings";

export const NODE_TYPE_MIME = "application/x-lcm-node-type";

const HEAD = 30;
const SUMMARY = 24;
const PORT_H = 20;

interface GraphNodeData extends Record<string, unknown> {
  id: string;
  type: string;
  color: string;
  summary: string;
  ports: string[];
  hasInput: boolean;
  status?: NodeStatus;
  issues: number;
  editable: boolean;
}

type GraphNode = Node<GraphNodeData, "policy">;

function summarize(node: ModelNode, schema: NodeType | undefined): string {
  const order = [...(schema?.props.map((p) => p.name) ?? []), ...Object.keys(node.props)];
  for (const name of order) {
    const v = node.props[name];
    if (v === undefined || v === null || v === "") continue;
    const text = typeof v === "string" ? v : JSON.stringify(v);
    return `${name}: ${text.length > 40 ? `${text.slice(0, 39)}…` : text}`;
  }
  return "";
}

const sameNodeData = (a: GraphNodeData, b: GraphNodeData) =>
  a.id === b.id &&
  a.type === b.type &&
  a.color === b.color &&
  a.summary === b.summary &&
  a.hasInput === b.hasInput &&
  a.status === b.status &&
  a.issues === b.issues &&
  a.editable === b.editable &&
  a.ports.length === b.ports.length &&
  a.ports.every((p, i) => p === b.ports[i]);

const PolicyNodeView = memo(
  function PolicyNodeView({ data, selected }: NodeProps<GraphNode>) {
    const st = data.status ? nodeStatusView(data.status) : undefined;
    const cls = ["gnode", selected ? "selected" : "", data.issues ? "has-issue" : "", st ? `st-${st.tone}` : ""].filter(Boolean).join(" ");
    return (
      <div className={cls} style={{ ["--cat" as string]: data.color }} data-node-id={data.id}>
        {data.hasInput && <Handle type="target" position={HandlePosition.Left} id="in" isConnectable={data.editable} style={{ top: HEAD / 2 + 2 }} />}
        <div className="gh">
          <div style={{ minWidth: 0 }}>
            <div className="gid" title={data.id}>
              {data.id}
            </div>
            <div className="gtype" title={data.type}>
              {data.type}
            </div>
          </div>
          {data.issues > 0 && (
            <span className="badge-issue gstat" title={`${data.issues}`} aria-label={`${s.editor.issues}: ${data.issues}`}>
              ⚠ {data.issues}
            </span>
          )}
          {st && (
            <span className={`pill ${st.tone} gstat`}>
              <span className="icon" aria-hidden="true">
                {st.icon}
              </span>
              {st.label}
            </span>
          )}
        </div>
        <div className="gsum" title={data.summary}>
          {data.summary}
        </div>
        {data.ports.map((p, i) => (
          <div key={p} className={`gport${p === "error" ? " err" : ""}`}>
            {p}
            <Handle
              type="source"
              position={HandlePosition.Right}
              id={p}
              isConnectable={data.editable}
              className={p === "error" ? "err" : undefined}
              style={{ top: HEAD + SUMMARY + i * PORT_H + PORT_H / 2 + 4 }}
            />
          </div>
        ))}
      </div>
    );
  },
  (a, b) => a.selected === b.selected && sameNodeData(a.data, b.data),
);

const nodeTypes = { policy: PolicyNodeView };

export interface GraphProps {
  model: PolicyModel;
  schemas: ReadonlyMap<string, NodeType>;
  positions: Readonly<Record<string, Position>>;
  selected?: string | null;
  onSelect?: (id: string | null) => void;
  issuesByNode?: ReadonlyMap<string, MappedIssue[]>;
  /** Run view: colours nodes and highlights the edges a run took. */
  statusById?: ReadonlyMap<string, NodeStatus>;
  takenEdges?: ReadonlySet<string>;
  /** Edit handlers; the graph is read-only without them. */
  onMove?: (positions: Record<string, Position>) => void;
  onConnect?: (from: string, port: string, to: string) => void;
  onDeleteEdge?: (edge: ModelEdge) => void;
  onDeleteNode?: (id: string) => void;
  onDropType?: (type: string, at: Position) => void;
}

function GraphInner(props: GraphProps) {
  const { model, schemas, positions, selected, onSelect, issuesByNode, statusById, takenEdges } = props;
  const editable = !!props.onMove;
  const rf = useReactFlow();

  const derived = useMemo<GraphNode[]>(
    () =>
      model.nodes.map((n) => {
        const schema = schemas.get(n.type);
        const data: GraphNodeData = {
          id: n.id,
          type: n.type,
          color: categoryColor(schema?.category ?? n.type.split(".")[0] ?? ""),
          summary: summarize(n, schema),
          ports: portsOf(schema, n, model.portsByNode.get(n.id)),
          hasInput: hasInputPort(schema),
          status: statusById?.get(n.id),
          issues: issuesByNode?.get(n.id)?.length ?? 0,
          editable,
        };
        return {
          id: n.id,
          type: "policy",
          position: positions[n.id] ?? { x: 0, y: 0 },
          data,
          selected: selected === n.id,
          draggable: editable,
          connectable: editable,
          deletable: editable,
        };
      }),
    [model, schemas, positions, selected, issuesByNode, statusById, editable],
  );

  // React Flow measures nodes and moves them while dragging through change
  // events, so it needs its own copy of the node list. The copy follows the
  // document-derived list and keeps the measurements of nodes that stay.
  const [nodes, setNodes] = useState<GraphNode[]>(derived);
  const [lastDerived, setLastDerived] = useState(derived);
  if (derived !== lastDerived) {
    setLastDerived(derived);
    setNodes((prev) => {
      const old = new Map(prev.map((n) => [n.id, n]));
      return derived.map((n) => {
        const o = old.get(n.id);
        return o ? { ...n, measured: o.measured, width: o.width, height: o.height } : n;
      });
    });
  }

  const edges = useMemo<Edge[]>(
    () =>
      model.edges.map((e) => {
        const err = e.port === "error";
        const run = !!takenEdges;
        const taken = takenEdges?.has(e.id) ?? false;
        const stroke = err ? "var(--destructive)" : run && taken ? "var(--success)" : "var(--muted-foreground)";
        return {
          id: e.id,
          source: e.from,
          sourceHandle: e.port,
          target: e.to,
          targetHandle: "in",
          label: e.port === "out" ? undefined : e.port,
          labelStyle: { fontSize: 10, fill: "var(--muted-foreground)" },
          labelBgStyle: { fill: "var(--background)" },
          style: { stroke, strokeWidth: run && taken ? 2.5 : 1.5, strokeDasharray: err ? "5 4" : undefined, opacity: run && !taken ? 0.35 : 1 },
          markerEnd: { type: MarkerType.ArrowClosed, color: stroke },
          deletable: editable,
        };
      }),
    [model.edges, takenEdges, editable],
  );

  const edgeById = useMemo(() => new Map(model.edges.map((e) => [e.id, e])), [model.edges]);

  const onNodesChange = useCallback((changes: NodeChange<GraphNode>[]) => setNodes((ns) => applyNodeChanges(changes, ns)), []);

  // A drag is written to the document once, when it ends.
  function commitDrag(_: unknown, __: GraphNode, dragged: GraphNode[]) {
    props.onMove?.(Object.fromEntries(dragged.map((n) => [n.id, n.position])));
  }

  function onConnect(c: Connection) {
    if (c.source && c.target && c.sourceHandle) props.onConnect?.(c.source, c.sourceHandle, c.target);
  }

  function onDragOver(e: DragEvent) {
    if (editable && e.dataTransfer.types.includes(NODE_TYPE_MIME)) {
      e.preventDefault();
      e.dataTransfer.dropEffect = "copy";
    }
  }

  function onDrop(e: DragEvent) {
    const type = e.dataTransfer.getData(NODE_TYPE_MIME);
    if (!type) return;
    e.preventDefault();
    props.onDropType?.(type, rf.screenToFlowPosition({ x: e.clientX, y: e.clientY }));
  }

  const many = model.nodes.length > 60;
  return (
    <div className="graph-wrap" onDragOver={onDragOver} onDrop={onDrop}>
      <ReactFlow<GraphNode>
        nodes={nodes}
        edges={edges}
        nodeTypes={nodeTypes}
        onNodesChange={onNodesChange}
        onNodeDragStop={commitDrag}
        onConnect={onConnect}
        onNodeClick={(_, n) => onSelect?.(n.id)}
        onPaneClick={() => onSelect?.(null)}
        onNodesDelete={(ns) => ns.forEach((n) => props.onDeleteNode?.(n.id))}
        onEdgesDelete={(es) =>
          es.forEach((e) => {
            const edge = edgeById.get(e.id);
            if (edge) props.onDeleteEdge?.(edge);
          })
        }
        deleteKeyCode={editable ? ["Backspace", "Delete"] : null}
        nodesDraggable={editable}
        nodesConnectable={editable}
        elementsSelectable
        onlyRenderVisibleElements={many}
        fitView
        fitViewOptions={{ padding: 0.2, maxZoom: 1 }}
        minZoom={0.1}
        colorMode="system"
        aria-label={s.run.graph}
      >
        <Background gap={20} />
        <Controls showInteractive={false} />
        {model.nodes.length > 30 && <MiniMap pannable zoomable ariaLabel={s.common.minimap} />}
      </ReactFlow>
    </div>
  );
}

export function PolicyGraph(props: GraphProps) {
  return (
    <ReactFlowProvider>
      <GraphInner {...props} />
    </ReactFlowProvider>
  );
}
