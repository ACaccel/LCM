/**
 * nodePortInfo.ts
 *
 * Port arity and palette seeds for a node type.
 *
 * The published schema is authoritative: GET /api/lcm/nodetypes carries every
 * registered node's port counts and branch labels, so a new backend node needs
 * no edit here. PORT_CONFIG below is only the offline fallback for when the
 * catalog has not loaded, and a stale entry there is a bug waiting to happen --
 * it used to pin Module to 1-in/1-out, which silently dropped an edge from any
 * policy whose module declared two output ports.
 *
 * Pure module, no React import.
 */

import type { ModuleConfig, NodeSchema } from './types';

export type PortConfig = {
  numInputPorts: number;
  numOutputPorts: number;
  // Human-readable labels per output port (index = port number).
  outputLabels?: string[];
};

const DEFAULT_PORTS: PortConfig = { numInputPorts: 1, numOutputPorts: 1 };

// Offline fallback only. Consulted when the schema catalog is unavailable.
const PORT_CONFIG: Record<string, PortConfig> = {
  Webhook:               { numInputPorts: 0, numOutputPorts: 1 },
  Timer:                 { numInputPorts: 0, numOutputPorts: 1 },
  If:                    { numInputPorts: 1, numOutputPorts: 2, outputLabels: ['true (0)', 'false (1)'] },
  Compare:               { numInputPorts: 2, numOutputPorts: 2, outputLabels: ['true (0)', 'false (1)'] },
  Merge:                 { numInputPorts: 2, numOutputPorts: 1 },
  Wait:                  DEFAULT_PORTS,
  HTTPRequest:           DEFAULT_PORTS,
  IEEE1935_AppStore:     DEFAULT_PORTS,
  IEEE1935_Application:  DEFAULT_PORTS,
  IEEE1935_Cluster:      DEFAULT_PORTS,
  IEEE1935_Deployment:   DEFAULT_PORTS,
  IEEE1935_Image:        DEFAULT_PORTS,
  IEEE1935_Jaeger:       DEFAULT_PORTS,
  IEEE1935_Machine:      DEFAULT_PORTS,
  IEEE1935_Manifest:     DEFAULT_PORTS,
  IEEE1935_Node:         DEFAULT_PORTS,
  IEEE1935_Service:      DEFAULT_PORTS,
  CALM_Telemetry:        DEFAULT_PORTS,
  CALM_PolicySelector:   {
    numInputPorts: 1,
    numOutputPorts: 4,
    outputLabels: ['latency_priority', 'balanced', 'energy_saving', 'resilient'],
  },
  CALM_PolicyMode:       DEFAULT_PORTS,
  CALM_PlacementApply:   { numInputPorts: 4, numOutputPorts: 1 },
  CALM_PostVerify:       DEFAULT_PORTS,
};

/**
 * Port config for a node type, schema first.
 *
 * `module` supplies the arity for a Module node, whose ports are declared per
 * instance in the service config rather than per type, so no schema can describe
 * them.
 */
export function getPortConfig(
  nodeType: string,
  schemas?: Map<string, NodeSchema>,
  module?: ModuleConfig,
): PortConfig {
  if (module) {
    return { numInputPorts: module.numInPort, numOutputPorts: module.numOutPort };
  }
  const schema = schemas?.get(nodeType);
  if (schema) {
    return {
      numInputPorts: schema.numInputPort,
      numOutputPorts: schema.numOutputPort,
      outputLabels: schema.outputLabels,
    };
  }
  // Copied, so a caller cannot write through to the shared table entry.
  return { ...(PORT_CONFIG[nodeType] ?? DEFAULT_PORTS) };
}

/**
 * True when the node type is a trigger.
 *
 * Getting this wrong is not cosmetic: flowToPolicyConfig files the node under
 * `node` instead of `trigger`, and the policy then fails to deploy.
 */
export function isTriggerType(nodeType: string, schemas?: Map<string, NodeSchema>): boolean {
  const schema = schemas?.get(nodeType);
  if (schema) return schema.kind === 'trigger';
  const cfg = PORT_CONFIG[nodeType];
  return cfg !== undefined && cfg.numInputPorts === 0;
}

// Palette seeds, shown until the catalog loads and unioned with it afterwards.
export const TRIGGER_TYPES: readonly string[] = ['Webhook', 'Timer'];
export const FUNCTION_TYPES: readonly string[] = [
  'If', 'Compare', 'Merge', 'Wait', 'HTTPRequest',
  'IEEE1935_AppStore', 'IEEE1935_Application', 'IEEE1935_Cluster',
  'IEEE1935_Deployment', 'IEEE1935_Image', 'IEEE1935_Jaeger', 'IEEE1935_Machine',
  'IEEE1935_Manifest', 'IEEE1935_Node', 'IEEE1935_Service',
  'CALM_Telemetry', 'CALM_PolicySelector', 'CALM_PolicyMode',
  'CALM_PlacementApply', 'CALM_PostVerify',
];
