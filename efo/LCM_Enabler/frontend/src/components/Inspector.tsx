/**
 * Inspector.tsx
 *
 * Right-panel inspector with three responsibilities:
 *  1. When a node is selected: schema-driven property editor.
 *  2. When an edge is selected: transFormat (src→dst) list editor.
 *  3. Policy operations: Deploy / Undeploy, Webhook Tester.
 *
 * Property editing works by cloning node data and calling setNodes — React
 * Flow's imperative API is used via the passed setter prop.
 */

import { useState, useEffect } from 'react';
import type { Node, Edge } from 'reactflow';
import type { CanvasNodeData, EdgeData } from '../model/types';
import SchemaPropertyForm from './SchemaPropertyForm';
import FieldMapper from './FieldMapper';
import { moduleOfNode, parseHandleIndex, renameProblem } from '../model/graphModel';
import { useNodeSchemas } from '../model/nodeSchemas';
import {
  deployPolicies,
  undeployPolicies,
  triggerWebhook,
  getPolicy,
} from '../api/lcmApi';
import { useStore } from '../hooks/useStore';
import { useServiceModules } from '../hooks/useServiceModules';
import { useRunStore } from '../hooks/useRunStore';
import { fieldChoices, namespacesAtInput } from '../model/namespaces';
import '../styles/Inspector.css';

type Props = {
  selectedNode: Node<CanvasNodeData> | null;
  selectedEdge: Edge<EdgeData> | null;
  onNodeDataChange: (nodeId: string, data: Partial<CanvasNodeData>) => void;
  /** Renames a node everywhere its name is meaningful; see graphModel. */
  onNodeRename: (oldId: string, newId: string) => void;
  onEdgeDataChange: (edgeId: string, data: EdgeData) => void;
  /** Starts polling the run trace after a webhook was dispatched. */
  onWebhookFired: () => void;
  /** All canvas nodes, so an edge's endpoints can be resolved for the mapper. */
  nodes: Node<CanvasNodeData>[];
  /** All canvas edges, so the mapper can walk back to the reachable namespaces. */
  edges: Edge<EdgeData>[];
};

export default function Inspector({
  selectedNode,
  selectedEdge,
  onNodeDataChange,
  onNodeRename,
  onEdgeDataChange,
  onWebhookFired,
  nodes,
  edges,
}: Props) {
  const { schemas, loaded: schemasLoaded } = useNodeSchemas();
  const modules = useServiceModules();
  // The last run is the only source of truth for a webhook payload's shape, so
  // the field suggestions merge it with what the schemas declare.
  const byNode = useRunStore((s) => s.byNode);
  // Held while the name field is being edited; see commitNodeName.
  const [pendingName, setPendingName] = useState<string | null>(null);
  const {
    selectedService,
    selectedPolicy,
    policyStatus,
    savedSinceDeploy,
    setPolicyStatus,
    pushToast,
  } = useStore();

  // ─── Webhook tester state ──────────────────────────────────────────────────
  const [webhookEventType, setWebhookEventType] = useState('');
  const [webhookContent, setWebhookContent] = useState('{}');
  const [webhookResult, setWebhookResult] = useState<string | null>(null);

  // Reset webhook event type when the selected node changes to a Webhook node.
  useEffect(() => {
    if (selectedNode?.data.nodeType === 'Webhook') {
      const ev = String(selectedNode.data.property['event_type'] ?? '');
      setWebhookEventType(ev);
    }
  }, [selectedNode]);

  // ─── Helpers ──────────────────────────────────────────────────────────────

  function updateProperty(key: string, value: unknown) {
    if (!selectedNode) return;
    onNodeDataChange(selectedNode.id, {
      property: { ...selectedNode.data.property, [key]: value },
    });
  }

  function updateNodeName(newName: string) {
    if (!selectedNode) return;
    setPendingName(newName);
  }

  /**
   * Applies a rename once the field is left, not on every keystroke.
   *
   * The name is the node's id, so renaming rewrites edges, carry lists and
   * qualified conditions; doing that per character would rewrite the graph
   * dozens of times and fight the caret.
   */
  function commitNodeName() {
    if (!selectedNode || pendingName === null) return;
    const name = pendingName.trim();
    setPendingName(null);
    if (name === selectedNode.id) return;
    const problem = renameProblem(nodes, selectedNode.id, name);
    if (problem) {
      pushToast(`Cannot rename: ${problem}`, 'error');
      return;
    }
    onNodeRename(selectedNode.id, name);
  }

  // A half-typed name must not follow the selection to another node.
  useEffect(() => {
    setPendingName(null);
  }, [selectedNode?.id]);

  // ─── Deploy / Undeploy ────────────────────────────────────────────────────

  async function handleDeploy() {
    if (!selectedService || !selectedPolicy) return;
    try {
      await deployPolicies({ SvcName: selectedService, Policies: [selectedPolicy] });
      // Re-fetch status to confirm.
      const res = await getPolicy(selectedService, selectedPolicy);
      // No canvas reload: deploying changes the engine's deployed set, not the
      // stored config, so there is nothing new to draw -- and rebuilding would
      // discard any edit not yet saved.
      setPolicyStatus(res.Status);
      pushToast(`Policy "${selectedPolicy}" deployed`, 'success');
    } catch (err) {
      pushToast(String(err), 'error');
    }
  }

  async function handleUndeploy() {
    if (!selectedService || !selectedPolicy) return;
    try {
      await undeployPolicies({ SvcName: selectedService, Policies: [selectedPolicy] });
      const res = await getPolicy(selectedService, selectedPolicy);
      setPolicyStatus(res.Status);
      pushToast(`Policy "${selectedPolicy}" undeployed`, 'success');
    } catch (err) {
      pushToast(String(err), 'error');
    }
  }

  // ─── Webhook tester ───────────────────────────────────────────────────────

  async function handleWebhookTest() {
    if (!selectedService) return;
    let content: unknown;
    try {
      content = JSON.parse(webhookContent) as unknown;
    } catch {
      pushToast('Webhook content must be valid JSON.', 'error');
      return;
    }
    try {
      const res = await triggerWebhook({
        SvcName: selectedService,
        EventType: webhookEventType,
        Content: content,
      });
      setWebhookResult(`dispatched: ${res.dispatched}`);
      pushToast(`Webhook dispatched to ${res.dispatched} trigger(s)`, 'success');
      // Only a dispatched event produces a run; otherwise there is nothing to poll.
      if (res.dispatched > 0) {
        onWebhookFired();
      }
    } catch (err) {
      pushToast(String(err), 'error');
      setWebhookResult(String(err));
    }
  }

  // ─── Edge editor ──────────────────────────────────────────────────────────

  /** Port index encoded in a React Flow handle id such as "src-1". */
  const portIndex = parseHandleIndex;

  function nodeById(id: string): Node<CanvasNodeData> | undefined {
    return nodes.find((n) => n.id === id);
  }

  /**
   * Field names a condition can name at one of this node's input ports.
   *
   * Both forms are offered: the bare name the flattening resolves, and the
   * qualified one for when two upstream nodes emit the same field and only one
   * survives the flattening.
   */
  function fieldChoicesAt(nodeId: string, port: number): string[] {
    return fieldChoices(namespacesAtInput(nodeId, port, nodes, edges, schemas, byNode));
  }

  /** True when either end of the edge is a module's own port. */
  function isBoundaryEdge(edge: Edge<EdgeData>): boolean {
    return Boolean(nodeById(edge.source)?.data.boundary ?? nodeById(edge.target)?.data.boundary);
  }

  // ─── Property form rendering (schema-driven) ──────────────────────────────

  function renderPropertyForm(node: Node<CanvasNodeData>) {
    const { nodeType, property } = node.data;
    const schema = schemas.get(nodeType);

    // A Module node is not in the published catalog: which modules exist is a
    // per-service question, so the choice is offered from the service's own
    // list rather than from a schema enum that could not be global.
    if (nodeType === 'Module') {
      return renderModulePicker(node);
    }

    if (schema) {
      return (
        <SchemaPropertyForm
          schema={schema}
          property={property}
          onChange={(k, v) => updateProperty(k, v)}
          fieldChoices={(port) => fieldChoicesAt(node.id, port)}
        />
      );
    }

    // No schema for this type (Module, or a type the server did not publish).
    // The raw editor stays as the escape hatch rather than showing nothing.
    return (
      <>
        {schemasLoaded && (
          <div className="inspector-sub-label">
            No schema for {nodeType} — editing raw key/value pairs.
          </div>
        )}
        <GenericPropertyEditor property={property} onChange={(k, v) => updateProperty(k, v)} />
      </>
    );
  }

  /**
   * Which module an instance runs, plus the port count that follows from it.
   *
   * Changing the module has to rewrite the node's arity in the same step: the
   * handles are drawn from it, and leaving a stale count would drop whichever
   * edges hang off the handles that disappear.
   */
  function renderModulePicker(node: Node<CanvasNodeData>) {
    const current = moduleOfNode({
      name: node.id,
      type: node.data.nodeType,
      property: node.data.property,
    });

    function pick(name: string) {
      const module = modules.find((m) => m.name === name);
      if (!module) return;
      onNodeDataChange(node.id, {
        property: { ...node.data.property, module: name },
        numInputPorts: module.numInPort,
        numOutputPorts: module.numOutPort,
      });
    }

    return (
      <div className="prop-group">
        <label>Module</label>
        <select value={current} onChange={(e) => pick(e.target.value)}>
          {!modules.some((m) => m.name === current) && (
            <option value={current}>⚠ {current} (not in this service)</option>
          )}
          {modules.map((m) => (
            <option key={m.name} value={m.name}>
              {m.name} ({m.numInPort} in / {m.numOutPort} out)
            </option>
          ))}
        </select>
        <div className="inspector-sub-label">
          Ports come from the module. Edit its contents from the Modules list.
        </div>
      </div>
    );
  }

  // ─── Render ───────────────────────────────────────────────────────────────

  const hasPolicy = Boolean(selectedService && selectedPolicy);
  const isDeployed = policyStatus === 'DEPLOYED';

  return (
    <div className="inspector">
      {selectedNode && (
        <section className="inspector-section">
          <div className="inspector-section-title">Node: {selectedNode.data.nodeType}</div>
          <div className="prop-group">
            <label>Name (id)</label>
            <input
              value={pendingName ?? selectedNode.data.label}
              onChange={(e) => updateNodeName(e.target.value)}
              onBlur={commitNodeName}
              onKeyDown={(e) => e.key === 'Enter' && commitNodeName()}
              title="The name is what downstream connections and conditions refer to; renaming moves them all."
            />
          </div>
          {renderPropertyForm(selectedNode)}
        </section>
      )}

      {selectedEdge && (
        <section className="inspector-section">
          <div className="inspector-section-title">
            Edge: {selectedEdge.source} → {selectedEdge.target}
          </div>
          <div className="inspector-sub-label">
            Port {portIndex(selectedEdge.sourceHandle)} → Port {portIndex(selectedEdge.targetHandle)}
          </div>
          {isBoundaryEdge(selectedEdge) ? (
            // A module port binds to an inner node and nothing else: the stored
            // shape is {modulePort, nodeName, nodePort} with no room for a
            // mapping. The mapper would look like it worked and be discarded on
            // save, so it is not offered.
            <div className="fm-note">
              This connection binds a module port to an inner node. It carries the payload
              through unchanged — mapping and context selection belong on the connections
              inside the module.
            </div>
          ) : (
            <FieldMapper
              edge={selectedEdge}
              sourceNode={nodeById(selectedEdge.source)}
              targetNode={nodeById(selectedEdge.target)}
              sourcePort={portIndex(selectedEdge.sourceHandle)}
              targetPort={portIndex(selectedEdge.targetHandle)}
              nodes={nodes}
              edges={edges}
              onChange={(data) => onEdgeDataChange(selectedEdge.id, data)}
            />
          )}
        </section>
      )}

      {hasPolicy && (
        <section className="inspector-section">
          <div className="inspector-section-title">Policy Controls</div>
          <div className="status-badge" data-status={isDeployed ? 'deployed' : 'undeployed'}>
            {isDeployed ? 'DEPLOYED' : 'UNDEPLOYED'}
          </div>
          {isDeployed && savedSinceDeploy && (
            <div className="fm-note">
              The running policy is still the version you deployed. Node properties and edge
              field mappings are read when a policy is deployed, so undeploy and deploy again
              to apply what you just saved.
            </div>
          )}
          <div className="btn-row">
            <button
              className="btn-primary"
              disabled={isDeployed}
              onClick={() => void handleDeploy()}
            >
              Deploy
            </button>
            <button
              className="btn-danger"
              disabled={!isDeployed}
              onClick={() => void handleUndeploy()}
            >
              Undeploy
            </button>
          </div>
        </section>
      )}

      {hasPolicy && (
        <section className="inspector-section">
          <div className="inspector-section-title">Webhook Tester</div>
          <div className="prop-group">
            <label>EventType</label>
            <input
              value={webhookEventType}
              onChange={(e) => setWebhookEventType(e.target.value)}
              placeholder="e.g. scale"
            />
          </div>
          <div className="prop-group">
            <label>Content (JSON)</label>
            <textarea
              value={webhookContent}
              onChange={(e) => setWebhookContent(e.target.value)}
              rows={3}
            />
          </div>
          <button className="btn-primary" onClick={() => void handleWebhookTest()}>
            Fire Webhook
          </button>
          {webhookResult && <div className="code-result">{webhookResult}</div>}
        </section>
      )}

      {!selectedNode && !selectedEdge && !hasPolicy && (
        <div className="inspector-empty">Select a node or edge to inspect it.</div>
      )}
    </div>
  );
}

// ─── Generic property editor ──────────────────────────────────────────────────

type GenericPropEditorProps = {
  property: Record<string, unknown>;
  onChange: (key: string, value: unknown) => void;
};

function GenericPropertyEditor({ property, onChange }: GenericPropEditorProps) {
  const [newKey, setNewKey] = useState('');
  const [newVal, setNewVal] = useState('');

  function addRow() {
    if (!newKey.trim()) return;
    onChange(newKey.trim(), newVal);
    setNewKey('');
    setNewVal('');
  }

  return (
    <div className="generic-prop-editor">
      {Object.entries(property).map(([k, v]) => (
        <div key={k} className="prop-group">
          <label>{k}</label>
          <input
            value={String(v)}
            onChange={(e) => onChange(k, e.target.value)}
          />
        </div>
      ))}
      <div className="tf-row">
        <input
          placeholder="key"
          value={newKey}
          onChange={(e) => setNewKey(e.target.value)}
          style={{ flex: '0 0 80px' }}
        />
        <span className="tf-arrow">=</span>
        <input
          placeholder="value"
          value={newVal}
          onChange={(e) => setNewVal(e.target.value)}
        />
        <button className="icon-btn" onClick={addRow} title="Add property">+</button>
      </div>
    </div>
  );
}
