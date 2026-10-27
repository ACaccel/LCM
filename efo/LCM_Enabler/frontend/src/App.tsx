/**
 * App.tsx
 *
 * Root component — three-pane layout:
 *   Left:   ServicePanel (service list + policy list) + NodePalette
 *   Center: PolicyCanvas (React Flow editor)
 *   Right:  Inspector (property form + policy operations)
 *
 * State ownership:
 *  - nodes / edges live here so both PolicyCanvas (rendering) and Inspector
 *    (editing) can read and write without coupling.
 *  - selectedNode / selectedEdge bubble up from PolicyCanvas via
 *    onSelectionChange.
 *  - addNode is a function formed here and passed to NodePalette; it writes
 *    directly into setNodes.
 */

import { useCallback, useState } from 'react';
import { useNodesState, useEdgesState, type Node, type Edge } from 'reactflow';
import ServicePanel from './components/ServicePanel';
import NodePalette from './components/NodePalette';
import PolicyCanvas from './components/PolicyCanvas';
import Inspector from './components/Inspector';
import ToastArea from './components/ToastArea';
import RunPanel from './components/RunPanel';
import { useRunPolling } from './hooks/useRunPolling';
import { useStore } from './hooks/useStore';
import { useServiceModules } from './hooks/useServiceModules';
import { getPortConfig, isTriggerType } from './model/nodePortInfo';
import { moduleInstance } from './model/graphModel';
import { renameNode } from './model/graphModel';
import { useNodeSchemas } from './model/nodeSchemas';
import ErrorBoundary from './components/ErrorBoundary';
import type { CanvasNodeData, EdgeData } from './model/types';
import './styles/App.css';

export default function App() {
  // Shared canvas state — owned here, passed into PolicyCanvas as props.
  const [nodes, setNodes, onNodesChange] = useNodesState<CanvasNodeData>([]);
  const [edges, setEdges, onEdgesChange] = useEdgesState<EdgeData>([]);

  // Currently selected node / edge (set by canvas selection events).
  const [selectedNode, setSelectedNode] = useState<Node<CanvasNodeData> | null>(null);
  const [selectedEdge, setSelectedEdge] = useState<
    Edge<EdgeData> | null
  >(null);

  // The run panel and the webhook tester share one polling loop, owned here so
  // both the canvas overlay and the panel read the same trace.
  const { selectedService, selectedPolicy, selectedModule } = useStore();
  const startRunPolling = useRunPolling(selectedService, selectedPolicy);

  // Port arity for a newly placed node comes from the published schema.
  const { schemas } = useNodeSchemas();
  // A Module node's arity does not: it is declared per service, so the palette
  // needs the module list to offer instances at all.
  const modules = useServiceModules();

  // ─── Selection handler ────────────────────────────────────────────────────

  const handleSelectionChange = useCallback(
    (
      node: Node<CanvasNodeData> | null,
      edge: Edge<EdgeData> | null,
    ) => {
      setSelectedNode(node);
      setSelectedEdge(edge);
    },
    [],
  );

  // ─── Inspector → node data mutation ──────────────────────────────────────

  const handleNodeDataChange = useCallback(
    (nodeId: string, data: Partial<CanvasNodeData>) => {
      setNodes((prev) =>
        prev.map((n) => (n.id === nodeId ? { ...n, data: { ...n.data, ...data } } : n)),
      );
      // Keep selectedNode in sync so the Inspector re-renders.
      setSelectedNode((prev) =>
        prev && prev.id === nodeId ? { ...prev, data: { ...prev.data, ...data } } : prev,
      );
    },
    [setNodes],
  );

  // ─── Inspector → node rename ─────────────────────────────────────────────

  const handleNodeRename = useCallback(
    (oldId: string, newId: string) => {
      // Nodes and edges move in one step: the id is the node's name on the
      // wire, and edges, carry lists and qualified conditions all point at it
      // by that name. Computed from the current values rather than inside a
      // state updater, because an updater has to stay free of side effects.
      const renamed = renameNode(nodes, edges, oldId, newId);
      setNodes(renamed.nodes);
      setEdges(renamed.edges);
      setSelectedNode((prev) =>
        prev && prev.id === oldId
          ? { ...prev, id: newId, data: { ...prev.data, label: newId } }
          : prev,
      );
    },
    [nodes, edges, setNodes, setEdges],
  );

  // ─── Inspector → edge data mutation ──────────────────────────────────────

  const handleEdgeDataChange = useCallback(
    (edgeId: string, data: EdgeData) => {
      setEdges((prev) => prev.map((e) => (e.id === edgeId ? { ...e, data } : e)));
      // Keep selectedEdge in sync so the Inspector re-renders.
      setSelectedEdge((prev) => (prev && prev.id === edgeId ? { ...prev, data } : prev));
    },
    [setEdges],
  );

  // ─── Palette → add node at centre ────────────────────────────────────────

  const handleAddNode = useCallback(
    (nodeType: string) => {
      const portCfg = getPortConfig(nodeType, schemas);
      const kind = isTriggerType(nodeType, schemas) ? 'trigger' : 'function';

      setNodes((ns) => {
        const existing = ns.filter((n) => n.data.nodeType === nodeType);
        const baseName = nodeType.replace('IEEE1935_', '').toLowerCase();
        const name = `${baseName}_${existing.length + 1}`;
        const newNode: Node<CanvasNodeData> = {
          id: name,
          type: 'lcmNode',
          position: { x: 120 + ns.length * 30, y: 80 + ns.length * 40 },
          data: {
            label: name,
            nodeType,
            kind,
            property: {},
            numInputPorts: portCfg.numInputPorts,
            numOutputPorts: portCfg.numOutputPorts,
            outputLabels: portCfg.outputLabels,
          },
        };
        return [...ns, newNode];
      });
    },
    [schemas, setNodes],
  );

  // A module instance is placed the same way as any other node. It could only
  // be dragged before, which was an inconsistency rather than a rule: the
  // reason a module is not a bare palette type is that its arity comes from
  // the module it names, and clicking carries that name just as well as a drag.
  const handleAddModule = useCallback(
    (moduleName: string) => {
      const module = modules.find((m) => m.name === moduleName);
      if (!module) return;
      setNodes((ns) =>
        [...ns, moduleInstance(module, ns, { x: 120 + ns.length * 30, y: 80 + ns.length * 40 })],
      );
    },
    [modules, setNodes],
  );

  return (
    <div className="app-root">
      <header className="app-header">
        <span className="app-title">IEEE 1935 LCM Policy Designer</span>
      </header>

      <div className="app-body">
        <aside className="left-panel">
          <ServicePanel />
          <NodePalette
            onAddNode={handleAddNode}
            onAddModule={handleAddModule}
            modules={modules}
          />
        </aside>

        <main className="center-panel">
          {/* Scoped per panel so a bad value in one cannot blank the others. */}
          <ErrorBoundary label="canvas">
            <PolicyCanvas
              nodes={nodes}
              edges={edges}
              setNodes={setNodes}
              setEdges={setEdges}
              onNodesChange={onNodesChange}
              onEdgesChange={onEdgesChange}
              onSelectionChange={handleSelectionChange}
            />
          </ErrorBoundary>
          {/* A module is not deployable on its own, so it has no run to show. */}
          {!selectedModule && (
            <ErrorBoundary label="run panel">
              <RunPanel onRefresh={startRunPolling} />
            </ErrorBoundary>
          )}
        </main>

        <aside className="right-panel">
          <ErrorBoundary label="inspector">
            <Inspector
              selectedNode={selectedNode}
              selectedEdge={selectedEdge}
              onNodeDataChange={handleNodeDataChange}
              onNodeRename={handleNodeRename}
              onEdgeDataChange={handleEdgeDataChange}
              onWebhookFired={startRunPolling}
              nodes={nodes}
              edges={edges}
            />
          </ErrorBoundary>
        </aside>
      </div>

      <ToastArea />
    </div>
  );
}
