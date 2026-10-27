/**
 * PolicyCanvas.tsx
 *
 * Central React Flow canvas that edits a single PolicyConfig graph.
 *
 * State design: nodes and edges are OWNED by App (via PolicyCanvasWithRef)
 * and passed in as props. This allows the Inspector to mutate node/edge data
 * and have it reflected in the canvas without React Flow owning the state.
 *
 * Responsibilities:
 *  - Load an existing policy via GET /api/lcm/service/:name/policy/:pname
 *    and call the setNodes/setEdges props to populate the canvas.
 *  - Allow drag-drop from the palette (drop event writes to setNodes).
 *  - Allow connecting nodes (addEdge writes to setEdges).
 *  - Save: convert current nodes+edges to PolicyConfig and POST/PUT.
 */

import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type DragEvent,
} from 'react';
import ReactFlow, {
  Background,
  Controls,
  MiniMap,
  addEdge,
  type Connection,
  type Edge,
  type Node,
  type ReactFlowInstance,
  type OnNodesChange,
  type OnEdgesChange,
} from 'reactflow';
import 'reactflow/dist/style.css';

import LcmNode from './LcmNode';
import ModuleBoundaryNode from './ModuleBoundaryNode';
import MappingEdge from './MappingEdge';
import { edgeStatus } from '../model/edgeStatus';
import { countIssues, validatePolicy, validateModule, type ValidationIssue } from '../model/validate';
import { useNodeSchemas } from '../model/nodeSchemas';
import {
  getPolicy,
  createPolicy,
  updatePolicy,
  getModule,
  createModule,
  updateModule,
} from '../api/lcmApi';
import {
  policyConfigToFlow,
  flowToPolicyConfig,
  emptyPolicyConfig,
  moduleConfigToFlow,
  flowToModuleConfig,
  emptyModuleConfig,
  moduleInstance,
  boundaryPortCount,
  addBoundaryPort,
  removeLastBoundaryPort,
  lastBoundaryPortProblem,
  parseHandleIndex,
} from '../model/graphModel';
import { applyPositions, collectPositions } from '../model/layout';
import { getPortConfig, isTriggerType } from '../model/nodePortInfo';
import { useStore } from '../hooks/useStore';
import { useServiceModules } from '../hooks/useServiceModules';
import { useRunStore, edgeKey } from '../hooks/useRunStore';
import type { CanvasNodeData, CanvasPoint, EdgeData } from '../model/types';
import '../styles/PolicyCanvas.css';

// Register the custom node types once outside the component.
const nodeTypes = { lcmNode: LcmNode, moduleBoundary: ModuleBoundaryNode };
const edgeTypes = { mapping: MappingEdge };

type Props = {
  nodes: Node<CanvasNodeData>[];
  edges: Edge<EdgeData>[];
  setNodes: React.Dispatch<React.SetStateAction<Node<CanvasNodeData>[]>>;
  setEdges: React.Dispatch<React.SetStateAction<Edge<EdgeData>[]>>;
  onNodesChange: OnNodesChange;
  onEdgesChange: OnEdgesChange;
  onSelectionChange: (
    node: Node<CanvasNodeData> | null,
    edge: Edge<EdgeData> | null,
  ) => void;
};

export default function PolicyCanvas({
  nodes,
  edges,
  setNodes,
  setEdges,
  onNodesChange,
  onEdgesChange,
  onSelectionChange,
}: Props) {
  const {
    selectedService,
    selectedPolicy,
    selectedModule,
    selectModule,
    canvasRevision,
    pushToast,
    policyStatus,
    setPolicyStatus,
    markSavedSinceDeploy,
    bumpCanvasRevision,
  } = useStore();
  // Selector-based so a polling tick does not re-render the whole canvas tree.
  const takenEdges = useRunStore((s) => s.takenEdges);
  const { schemas } = useNodeSchemas();
  const hasRun = useRunStore((s) => s.run?.hasRun ?? false);

  // Highlight the edges the run actually traversed and fade the rest. Derived at
  // render time rather than written back into edge state, so saving the policy
  // never persists run decoration.
  // Mapping status is decoration too: the underscore marks it derived-only, and
  // flowToPolicyConfig reads only transFormat, so it cannot reach the wire.
  const displayEdges = useMemo(() => {
    const nodeType = (id: string) => nodes.find((n) => n.id === id)?.data.nodeType ?? '';
    return edges.map((e) => {
      const status = edgeStatus(
        e.data?.transFormat ?? [],
        schemas.get(nodeType(e.source)),
        schemas.get(nodeType(e.target)),
      );
      const decorated = { ...e, type: 'mapping', data: { ...e.data, _status: status } };
      if (!hasRun) return decorated;

      const taken = takenEdges.has(edgeKey(e.source, parseHandleIndex(e.sourceHandle)));
      return taken
        ? { ...decorated, animated: true, style: { ...e.style, stroke: '#3aab6e', strokeWidth: 2 } }
        : { ...decorated, animated: false, style: { ...e.style, opacity: 0.3 } };
    });
  }, [edges, nodes, schemas, takenEdges, hasRun]);

  // One connection per port: a second one silently overwrites the first in the
  // engine and leaves the orphaned consumer blocked forever.
  const isValidConnection = useCallback(
    (conn: Connection): boolean => {
      if (!conn.source || !conn.target) return false;
      if (conn.source === conn.target) return false;
      if (edges.some((e) => e.source === conn.source && e.sourceHandle === conn.sourceHandle)) {
        return false;
      }
      if (edges.some((e) => e.target === conn.target && e.targetHandle === conn.targetHandle)) {
        return false;
      }
      return true;
    },
    [edges],
  );

  const [rfInstance, setRfInstance] = useState<ReactFlowInstance | null>(null);
  const [issues, setIssues] = useState<ValidationIssue[]>([]);
  const [issuesOpen, setIssuesOpen] = useState(false);
  const [isExisting, setIsExisting] = useState(false);
  // A Module node's arity is declared per service, so drawing one needs the
  // module list rather than a schema.
  const modules = useServiceModules();

  const editingModule = selectedModule !== null;
  const openName = selectedModule ?? selectedPolicy ?? '';

  // Track the last loaded combination to avoid duplicate network calls. The
  // module has to be part of the key: without it, policy -> module -> the same
  // policy reads as unchanged and leaves the module's nodes on the canvas.
  const loadedRef = useRef({ service: '', policy: '', module: '', revision: -1 });

  // A mirror of the live graph, kept in a ref rather than read as a dependency:
  // listing `nodes` on the load effect below would re-run a network effect on
  // every drag.
  const nodesRef = useRef<Node<CanvasNodeData>[]>(nodes);
  useEffect(() => {
    nodesRef.current = nodes;
  });

  // What is actually drawn, written when a load lands rather than when it
  // starts. loadedRef is claimed up front to suppress duplicate fetches, so it
  // says what is being loaded, not what is on screen.
  const renderedRef = useRef({ service: '', policy: '', module: '' });

  // ─── Load policy ──────────────────────────────────────────────────────────

  useEffect(() => {
    const key = {
      service: selectedService ?? '',
      policy: selectedPolicy ?? '',
      module: selectedModule ?? '',
      revision: canvasRevision,
    };
    if (
      key.service === loadedRef.current.service &&
      key.policy === loadedRef.current.policy &&
      key.module === loadedRef.current.module &&
      key.revision === loadedRef.current.revision
    ) {
      return;
    }
    loadedRef.current = key;

    // A revision bump reloads the item that is already open. What is on screen
    // is the arrangement the user is looking at, so it survives the rebuild --
    // including drags not yet saved, and policies stored before layouts were
    // persisted at all. A different item must not inherit it: node names repeat
    // across policies, and one policy's coordinates would land on another's
    // nodes.
    const sameItem =
      key.service === renderedRef.current.service &&
      key.policy === renderedRef.current.policy &&
      key.module === renderedRef.current.module;
    const carried = sameItem ? collectPositions(nodesRef.current) : new Map<string, CanvasPoint>();
    const rendered = { service: key.service, policy: key.policy, module: key.module };

    if (!selectedService || (!selectedPolicy && !selectedModule)) {
      setNodes([]);
      setEdges([]);
      setIsExisting(false);
      renderedRef.current = rendered;
      return;
    }

    if (selectedModule) {
      void getModule(selectedService, selectedModule)
        .then((module) => {
          const { nodes: n, edges: e } = moduleConfigToFlow(module, schemas, modules);
          setNodes(applyPositions(n, carried));
          setEdges(e);
          setIsExisting(true);
          renderedRef.current = rendered;
        })
        .catch(() => {
          // New module — one port each way, nothing wired yet.
          const { nodes: n, edges: e } = moduleConfigToFlow(
            emptyModuleConfig(selectedModule),
            schemas,
            modules,
          );
          setNodes(applyPositions(n, carried));
          setEdges(e);
          setIsExisting(false);
          renderedRef.current = rendered;
        });
      return;
    }

    void getPolicy(selectedService, selectedPolicy as string)
      .then((res) => {
        const { nodes: n, edges: e } = policyConfigToFlow(res.Config, schemas, modules);
        setNodes(applyPositions(n, carried));
        setEdges(e);
        setIsExisting(true);
        setPolicyStatus(res.Status);
        renderedRef.current = rendered;
      })
      .catch(() => {
        // New policy — start with a blank canvas.
        const blank = emptyPolicyConfig(selectedPolicy as string);
        const { nodes: n, edges: e } = policyConfigToFlow(blank, schemas);
        setNodes(applyPositions(n, carried));
        setEdges(e);
        setIsExisting(false);
        setPolicyStatus('UNDEPLOYED');
        renderedRef.current = rendered;
      });
  }, [
    selectedService,
    selectedPolicy,
    selectedModule,
    selectModule,
    canvasRevision,
    schemas,
    modules,
    setNodes,
    setEdges,
    setPolicyStatus,
  ]);

  // ─── Connections ──────────────────────────────────────────────────────────

  const onConnect = useCallback(
    (conn: Connection) => {
      setEdges((eds) =>
        addEdge({ ...conn, data: { transFormat: [], carry: [] }, type: 'mapping' }, eds),
      );
    },
    [setEdges],
  );

  // ─── Selection relay ──────────────────────────────────────────────────────

  const handleSelectionChange = useCallback(
    ({ nodes: selNodes, edges: selEdges }: { nodes: Node[]; edges: Edge[] }) => {
      const selNode = selNodes.length === 1 ? (selNodes[0] as Node<CanvasNodeData>) : null;
      const selEdge =
        selEdges.length === 1
          ? (selEdges[0] as Edge<EdgeData>)
          : null;
      onSelectionChange(selNode, selEdge);
    },
    [onSelectionChange],
  );

  // ─── Drag-drop from palette ───────────────────────────────────────────────

  const onDragOver = useCallback((e: DragEvent<HTMLDivElement>) => {
    e.preventDefault();
    e.dataTransfer.dropEffect = 'copy';
  }, []);

  const onDrop = useCallback(
    (e: DragEvent<HTMLDivElement>) => {
      e.preventDefault();
      if (!rfInstance) return;
      const pos = rfInstance.screenToFlowPosition({ x: e.clientX, y: e.clientY });

      // A module is dragged by name, not as the bare "Module" type. Its port
      // count is declared per instance in the service config, so dropping a
      // typeless Module node would have to guess -- and guessing 1-in/1-out is
      // what silently dropped an edge the next time the policy was saved.
      const moduleName = e.dataTransfer.getData('application/lcm-module-name');
      if (moduleName) {
        const module = modules.find((m) => m.name === moduleName);
        if (!module) return;
        setNodes((ns) => [...ns, moduleInstance(module, ns, pos)]);
        return;
      }

      const nodeType = e.dataTransfer.getData('application/lcm-node-type');
      if (!nodeType) return;

      // For drag-drop, build the new node inline using statically imported helpers.
      const portCfg = getPortConfig(nodeType, schemas);
      const kind = isTriggerType(nodeType, schemas) ? 'trigger' : 'function';

      setNodes((ns) => {
        const existing = ns.filter((n) => n.data.nodeType === nodeType);
        const baseName = nodeType.replace('IEEE1935_', '').toLowerCase();
        const name = `${baseName}_${existing.length + 1}`;
        const newNode: Node<CanvasNodeData> = {
          id: name,
          type: 'lcmNode',
          position: pos,
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
    [rfInstance, schemas, modules, setNodes],
  );

  // ─── Module boundary ports ────────────────────────────────────────────────

  function addPort(side: 'in' | 'out') {
    setNodes((ns) => addBoundaryPort(side, ns as Node<CanvasNodeData>[]));
  }

  // Refusing is louder than silently doing nothing: the button stays clickable
  // so the reason can be shown, which is the only way to learn that the port
  // still has an edge on it.
  function removePort(side: 'in' | 'out') {
    const problem = lastBoundaryPortProblem(
      side,
      nodes as Node<CanvasNodeData>[],
      edges as Edge<EdgeData>[],
    );
    if (problem) {
      pushToast(problem, 'error');
      return;
    }
    setNodes((ns) => removeLastBoundaryPort(side, ns as Node<CanvasNodeData>[]));
  }

  // ─── Save ─────────────────────────────────────────────────────────────────

  async function handleSave() {
    if (!selectedService || !openName) {
      pushToast('Nothing is open to save.', 'error');
      return;
    }

    // Errors block the save; warnings do not. Both are listed in the toolbar
    // rather than pushed as toasts, because ToastArea runs one timer for the
    // newest toast and a burst would dismiss serially at four seconds apiece.
    const found = editingModule
      ? validateModule(nodes as Node<CanvasNodeData>[], edges as Edge<EdgeData>[], schemas)
      : validatePolicy(nodes as Node<CanvasNodeData>[], edges as Edge<EdgeData>[], schemas);
    setIssues(found);
    const { errors } = countIssues(found);
    if (errors > 0) {
      setIssuesOpen(true);
      pushToast(`Cannot save: ${errors} error${errors === 1 ? '' : 's'}`, 'error');
      return;
    }

    try {
      if (editingModule) {
        await saveModule();
      } else {
        await savePolicy();
      }
      bumpCanvasRevision();
    } catch (err) {
      pushToast(String(err), 'error');
    }
  }

  async function savePolicy() {
    const policyName = selectedPolicy as string;
    const config = flowToPolicyConfig(
      nodes as Node<CanvasNodeData>[],
      edges as Edge<EdgeData>[],
      policyName,
    );
    if (isExisting) {
      const res = await updatePolicy(selectedService as string, policyName, config);
      pushToast(res.msg ?? 'Policy updated', 'success');
    } else {
      const res = await createPolicy(selectedService as string, config);
      pushToast(res.msg ?? 'Policy created', 'success');
      setIsExisting(true);
    }
    // Saving writes the stored config. The engine only reads it at deploy time,
    // so a deployed policy keeps running the version it was deployed with until
    // it is redeployed.
    if (policyStatus === 'DEPLOYED') {
      markSavedSinceDeploy(true);
      pushToast('Saved — redeploy to apply this to the running policy', 'info');
    }
  }

  async function saveModule() {
    const moduleName = selectedModule as string;
    const config = flowToModuleConfig(
      nodes as Node<CanvasNodeData>[],
      edges as Edge<EdgeData>[],
      moduleName,
    );
    if (isExisting) {
      const res = await updateModule(selectedService as string, moduleName, config);
      pushToast(res.msg ?? 'Module updated', 'success');
    } else {
      const res = await createModule(selectedService as string, config);
      pushToast(res.msg ?? 'Module created', 'success');
      setIsExisting(true);
    }
    // A module is read at deploy time like everything else, so a policy already
    // running keeps the definition it was deployed with.
    pushToast('Redeploy any policy using this module to pick up the change', 'info');
  }

  /** Selects the node or edge an issue refers to, so the user can go fix it. */
  function selectIssueTarget(issue: ValidationIssue) {
    const target = issue.target;
    if (target.kind === 'node') {
      setNodes((ns) => ns.map((n) => ({ ...n, selected: n.id === target.id })));
      setEdges((es) => es.map((e) => ({ ...e, selected: false })));
    } else if (target.kind === 'edge') {
      setEdges((es) => es.map((e) => ({ ...e, selected: e.id === target.id })));
      setNodes((ns) => ns.map((n) => ({ ...n, selected: false })));
    }
  }

  const empty = !selectedService || !openName;

  return (
    <div className="policy-canvas-wrapper">
      {empty ? (
        <div className="canvas-empty">
          <p>Select or create a service, then open a policy or a module to begin editing.</p>
          <p className="canvas-hint">
            Tip: click a service name to expand it, then click a policy or module name to open it.
          </p>
        </div>
      ) : (
        <>
          <div className="canvas-toolbar">
            <span className="canvas-breadcrumb">
              {selectedService} ›{' '}
              {editingModule ? (
                <>
                  <button className="crumb-link" onClick={() => selectModule(null)}>
                    modules
                  </button>{' '}
                  › {openName}
                </>
              ) : (
                openName
              )}
            </span>
            {issues.length > 0 && (
              <button
                className="issue-summary"
                data-level={countIssues(issues).errors > 0 ? 'error' : 'warning'}
                onClick={() => setIssuesOpen((v) => !v)}
              >
                ⚠ {countIssues(issues).errors} error
                {countIssues(issues).errors === 1 ? '' : 's'} ·{' '}
                {countIssues(issues).warnings} warning
                {countIssues(issues).warnings === 1 ? '' : 's'}
              </button>
            )}
            {editingModule && (
              <span className="port-controls">
                {(['in', 'out'] as const).map((side) => (
                  <span key={side} className="port-control">
                    <span className="port-control-label">
                      {side === 'in' ? 'inputs' : 'outputs'} {boundaryPortCount(
                        nodes as Node<CanvasNodeData>[],
                        side,
                      )}
                    </span>
                    <button
                      className="btn-port"
                      title={`Add ${side === 'in' ? 'an input' : 'an output'} port`}
                      onClick={() => addPort(side)}
                    >
                      +
                    </button>
                    <button
                      className="btn-port"
                      title={
                        lastBoundaryPortProblem(
                          side,
                          nodes as Node<CanvasNodeData>[],
                          edges as Edge<EdgeData>[],
                        ) ?? `Remove the last ${side === 'in' ? 'input' : 'output'} port`
                      }
                      onClick={() => removePort(side)}
                    >
                      −
                    </button>
                  </span>
                ))}
              </span>
            )}
            <button className="btn-primary" onClick={() => void handleSave()}>
              {editingModule ? 'Save Module' : 'Save Policy'}
            </button>
          </div>
          {issuesOpen && issues.length > 0 && (
            <ul className="issue-list">
              {issues.map((issue, idx) => (
                <li
                  key={idx}
                  className={`issue-row ${issue.level}`}
                  onClick={() => selectIssueTarget(issue)}
                >
                  <span className="issue-level">{issue.level === 'error' ? '✕' : '!'}</span>
                  {issue.message}
                </li>
              ))}
            </ul>
          )}

          <div className="canvas-area" onDragOver={onDragOver} onDrop={onDrop}>
            <ReactFlow
              nodes={nodes}
              edges={displayEdges}
              nodeTypes={nodeTypes}
              edgeTypes={edgeTypes}
              isValidConnection={isValidConnection}
              onNodesChange={onNodesChange}
              onEdgesChange={onEdgesChange}
              onConnect={onConnect}
              onInit={setRfInstance}
              onSelectionChange={handleSelectionChange}
              fitView
              deleteKeyCode="Delete"
            >
              <Background color="#2a2f42" gap={20} />
              <Controls />
              <MiniMap
                nodeColor={(n) => {
                  const data = (n as Node<CanvasNodeData>).data;
                  return data?.kind === 'trigger' ? '#4a90d9' : '#3aab6e';
                }}
                style={{ background: '#1a1e2e' }}
              />
            </ReactFlow>
          </div>
        </>
      )}
    </div>
  );
}
