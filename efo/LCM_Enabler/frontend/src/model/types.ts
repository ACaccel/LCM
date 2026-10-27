// Core domain types mirroring the Go backend's ServiceConfig structure.

export type NodeConfig = {
  name: string;
  type: string;
  property: Record<string, unknown>;
};

export type TransFormatItem = {
  src: string;
  dst: string;
};

export type ConnectionUnit = {
  nodePair: [string, string];
  portPair: [number, number];
  transFormat: TransFormatItem[];
  /**
   * Upstream namespaces this connection forwards, in the order that decides
   * which one wins a field-name clash. Empty forwards everything, matching how
   * an empty transFormat means pass-through.
   */
  carry: string[];
};

// NOTE: PolicyConfig deliberately has no "type" field.
export type PolicyConfig = {
  name: string;
  trigger: NodeConfig[];
  node: NodeConfig[];
  path: ConnectionUnit[];
  ui?: CanvasLayout;
};

export type ModuleInOutPort = {
  modulePort: number;
  nodeName: string;
  nodePort: number;
};

export type ModuleConfig = {
  name: string;
  numInPort: number;
  numOutPort: number;
  inPort: ModuleInOutPort[];
  outPort: ModuleInOutPort[];
  node: NodeConfig[];
  path: ConnectionUnit[];
  ui?: CanvasLayout;
};

/** A node's position on the canvas. GUI-only; the engine never reads it. */
export type CanvasPoint = { x: number; y: number };

/**
 * Canvas layout stored with a policy or module; mirrors Go CanvasLayout.
 *
 * Keyed by canvas node id: a node's own name, and "__in:N" / "__out:N" for a
 * module's own ports. Optional throughout, so a config written by hand or by an
 * older build simply falls back to the automatic layout.
 */
export type CanvasLayout = { node?: Record<string, CanvasPoint> };

export type ServiceConfig = {
  service_name: string;
  module: ModuleConfig[];
  policy: PolicyConfig[];
};

// API response shapes from GET /api/lcm/nodetypes
export type NodeTypeTrigger = {
  type: string;
  triggerType: string;
};

export type NodeTypeFunction = {
  type: string;
  numInputPort: number;
  numOutputPort: number;
};

/*
 * Node schema
 *
 * Mirrors what GET /api/lcm/nodetypes returns under `schemas`. Field names are
 * the runtime map keys, so they can be used verbatim in a transFormat mapping
 * or a property object.
 */

export type FieldType =
  | 'string'
  | 'integer'
  | 'number'
  | 'boolean'
  | 'string[]'
  | 'array'
  | 'object'
  | 'object[]'
  | 'unknown';

export type FieldSchema = {
  name: string;
  type: FieldType;
  /** Closed set of legal values; render a dropdown when present. */
  enum?: string[];
  required?: boolean;
  secret?: boolean;
  filePath?: boolean;
  /**
   * True when the value names a field of the incoming payload rather than
   * being a value itself, such as the field an If condition reads.
   */
  fieldRef?: boolean;
  /** Input port the reference reads; meaningful only with fieldRef. */
  fieldRefPort?: number;
  doc?: string;
  /** Discriminator values that use this property; also the ones requiring it. */
  showWhen?: string[];
  /**
   * One element's fields when type is 'object[]', so the form can render a
   * repeatable row editor. Properties only, and one level deep.
   */
  item?: FieldSchema[];
};

export type PortSchema = {
  fields: FieldSchema[];
  /** Shape decided at runtime (a webhook payload): offer free text, not a list. */
  dynamic?: boolean;
};

export type NodeSchema = {
  type: string;
  kind: 'trigger' | 'function';
  numInputPort: number;
  numOutputPort: number;
  outputLabels?: string[];
  /** Property whose value gates showWhen, e.g. "request". */
  discriminator?: string;
  properties: FieldSchema[];
  /** null when the node reads no input at all; an empty field list when it declares an empty one. */
  input: PortSchema | null;
  output: PortSchema | null;
  /** Emits no namespace of its own: what leaves it is exactly what arrived. */
  passthrough?: boolean;
};

export type NodeTypesResponse = {
  triggers: NodeTypeTrigger[];
  functions: NodeTypeFunction[];
  schemas: NodeSchema[];
};

export type PolicyStatus = 'DEPLOYED' | 'UNDEPLOYED';

export type PolicyStatusResponse = {
  Name: string;
  Status: PolicyStatus;
  Config: PolicyConfig;
};

export type DeployRequest = {
  SvcName: string;
  Policies: string[];
};

export type WebhookRequest = {
  SvcName: string;
  EventType: string;
  Content: unknown;
};

export type WebhookResponse = {
  dispatched: number;
};

export type TemplateRequest = {
  SvcName: string;
  TemplateName: string;
};

/**
 * Response of the config-file upload endpoints (POST/PUT /api/lcm/service/file).
 * snake_case on purpose: the backend answers with the config-side key style so
 * the GUI can open what was just uploaded without re-parsing the file.
 * `policies` preserves document order; the first entry is what gets opened.
 */
export type UploadServiceConfigResponse = {
  msg?: string;
  service_name: string;
  policies: string[];
};

// Canvas node metadata used by the graph model layer.
// "kind" distinguishes triggers from function nodes.
export type CanvasNodeKind = 'trigger' | 'function';

/**
 * A module's own port, drawn as a node so that binding it to an inner node is
 * ordinary edge drawing rather than a separate form.
 *
 * This is a separate field rather than a third CanvasNodeKind on purpose: the
 * trigger/function split is what flowToPolicyConfig buckets on, and widening it
 * there would silently file a boundary pseudo-node into a policy's node list.
 */
export type ModuleBoundary = {
  side: 'in' | 'out';
  port: number;
};

export type CanvasNodeData = {
  label: string;
  nodeType: string;
  kind: CanvasNodeKind;
  property: Record<string, unknown>;
  numInputPorts: number;
  numOutputPorts: number;
  /** Branch names per output port, e.g. If's true/false. */
  outputLabels?: string[];
  /** Set only on the pseudo-nodes standing for a module's own ports. */
  boundary?: ModuleBoundary;
};

/**
 * What an edge carries on the canvas.
 *
 * transFormat renames fields for the receiving node; carry decides which
 * upstream namespaces continue downstream. They are different jobs: renaming a
 * field for one node does not change what the rest of the policy still sees
 * under the original name.
 */
export type EdgeData = {
  transFormat: TransFormatItem[];
  carry: string[];
};

/*
 * Execution trace
 *
 * Mirrors the RunTrace the backend returns from
 * GET /api/lcm/service/{svc}/policy/{policy}/run.
 */

export type RunStatus = 'running' | 'complete' | 'no-run';

export type RunTrigger = {
  node: string;
  nodeType: string;
  content: Record<string, unknown> | null;
};

export type WaitingNode = {
  node: string;
  scope: string;
  port: number;
};

/**
 * One node execution. input, inputRaw and inputErrors are parallel arrays
 * indexed by input port: input is what the node received after transFormat was
 * applied (equal to the upstream payload when no transFormat is configured, a
 * projection of it otherwise), inputRaw is always the untransformed upstream
 * payload, and inputErrors reports transform failures the engine would
 * otherwise only write to its log.
 */
export type RunNodeEvent = {
  seq: number;
  node: string;
  nodeType: string;
  /** Empty for a top-level node; the module path for a module-internal one. */
  scope: string;
  inPort: number;
  startOffsetMs: number;
  durationMs: number;
  input: unknown[];
  inputRaw: (Record<string, unknown> | null)[];
  inputErrors: string[];
  output: Record<string, unknown>[];
  /** Output ports the node selected, i.e. the edges actually taken. */
  ports: number[];
  status: string;
};

export type RunTrace = {
  policy: string;
  hasRun: boolean;
  runId: string;
  trigger: RunTrigger;
  startedAt: string;
  updatedAt: string;
  durationMs: number;
  status: RunStatus;
  active: number;
  blocked: number;
  waiting: WaitingNode[];
  eventCount: number;
  since: number;
  truncated: boolean;
  events: RunNodeEvent[];
};
