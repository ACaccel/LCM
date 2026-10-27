/**
 * validate.ts
 *
 * Checks a policy graph before it is saved, so misconfigurations surface while
 * the user is still looking at the canvas rather than as a silent 400 or a run
 * that never completes.
 *
 * Pure module, no React import, so it can be unit-tested as-is.
 */

import { mappingProblems } from './edgeStatus';
import { toRowList } from './coerce';
import { isEmptyValue, visibleProperties } from './nodeSchemas';
import { parseHandleIndex } from './graphModel';
import type { CanvasNodeData, EdgeData, FieldSchema, NodeSchema } from './types';
import type { Edge, Node } from 'reactflow';

export type IssueTarget =
  | { kind: 'node'; id: string }
  | { kind: 'edge'; id: string }
  | { kind: 'policy' };

export type ValidationIssue = {
  level: 'error' | 'warning';
  target: IssueTarget;
  message: string;
};

type GraphNode = Node<CanvasNodeData>;
type GraphEdge = Edge<EdgeData>;

export function validatePolicy(
  nodes: GraphNode[],
  edges: GraphEdge[],
  schemas: Map<string, NodeSchema>,
): ValidationIssue[] {
  const issues = graphIssues(nodes, edges, schemas);

  if (!nodes.some((n) => n.data.kind === 'trigger')) {
    issues.push({
      level: 'warning',
      target: { kind: 'policy' },
      message: 'the policy has no trigger, so nothing can start it',
    });
  }

  issues.push(...unfedInputIssues(nodes, edges));
  return issues;
}

/**
 * Checks a module graph.
 *
 * It shares every graph-level rule with a policy and differs in two: a module
 * legitimately has no trigger, and its boundary must be complete. The second is
 * an error rather than a warning because an unwired boundary port leaves a nil
 * inner node that the engine dereferences from a walker goroutine, where nothing
 * recovers -- the whole process goes down at run time. The engine refuses it at
 * deploy too; catching it here is what keeps the designer from getting that far.
 */
export function validateModule(
  nodes: GraphNode[],
  edges: GraphEdge[],
  schemas: Map<string, NodeSchema>,
): ValidationIssue[] {
  const inner = nodes.filter((n) => !n.data.boundary);
  const issues = graphIssues(inner, edges, schemas);

  for (const node of nodes) {
    const boundary = node.data.boundary;
    if (!boundary) continue;
    const wired =
      boundary.side === 'in'
        ? edges.some((e) => e.source === node.id)
        : edges.some((e) => e.target === node.id);
    if (!wired) {
      issues.push({
        level: 'error',
        target: { kind: 'node', id: node.id },
        message: `module ${boundary.side}put port ${boundary.port} is not wired to an inner node`,
      });
    }
  }

  issues.push(...unfedInputIssues(inner, edges));
  return issues;
}

/** The rules a policy and a module share. */
function graphIssues(
  nodes: GraphNode[],
  edges: GraphEdge[],
  schemas: Map<string, NodeSchema>,
): ValidationIssue[] {
  const issues: ValidationIssue[] = [];
  const byId = new Map(nodes.map((n) => [n.id, n]));

  issues.push(...portIssues(edges));
  issues.push(...duplicateNodeIssues(nodes));

  for (const edge of edges) {
    const source = byId.get(edge.source);
    const target = byId.get(edge.target);
    // An edge touching a boundary pseudo-node carries no mapping at all, so
    // there is nothing here to check.
    if (!source && !target) continue;
    const problems = mappingProblems(
      edge.data?.transFormat ?? [],
      source && schemas.get(source.data.nodeType),
      target && schemas.get(target.data.nodeType),
    );
    for (const message of problems) {
      // A src the source does not produce is a hard transform error at run
      // time; a dst the target ignores merely wastes the mapping.
      const level: ValidationIssue['level'] = message.includes('not read by the target')
        ? 'warning'
        : 'error';
      issues.push({ level, target: { kind: 'edge', id: edge.id }, message });
    }
  }

  for (const node of nodes) {
    issues.push(...propertyIssues(node, schemas.get(node.data.nodeType)));
  }
  return issues;
}

/** One connection per port; the engine cannot serve two consumers from one. */
function portIssues(edges: GraphEdge[]): ValidationIssue[] {
  const issues: ValidationIssue[] = [];
  const outputs = new Map<string, string>();
  const inputs = new Map<string, string>();

  for (const edge of edges) {
    const outKey = `${edge.source}:${parseHandleIndex(edge.sourceHandle)}`;
    if (outputs.has(outKey)) {
      issues.push({
        level: 'error',
        target: { kind: 'edge', id: edge.id },
        message: `${edge.source} output port ${parseHandleIndex(edge.sourceHandle)} already feeds ${outputs.get(outKey)}; a port can only feed one node`,
      });
    } else {
      outputs.set(outKey, edge.target);
    }

    const inKey = `${edge.target}:${parseHandleIndex(edge.targetHandle)}`;
    if (inputs.has(inKey)) {
      issues.push({
        level: 'error',
        target: { kind: 'edge', id: edge.id },
        message: `${edge.target} input port ${parseHandleIndex(edge.targetHandle)} is already fed by ${inputs.get(inKey)}`,
      });
    } else {
      inputs.set(inKey, edge.source);
    }
  }
  return issues;
}

function duplicateNodeIssues(nodes: GraphNode[]): ValidationIssue[] {
  const seen = new Set<string>();
  const issues: ValidationIssue[] = [];
  for (const node of nodes) {
    if (seen.has(node.id)) {
      issues.push({
        level: 'error',
        target: { kind: 'node', id: node.id },
        message: `duplicate node name "${node.id}"`,
      });
    }
    seen.add(node.id);
  }
  return issues;
}

function propertyIssues(node: GraphNode, schema: NodeSchema | undefined): ValidationIssue[] {
  if (!schema) return [];
  const issues: ValidationIssue[] = [];
  const property = node.data.property;

  for (const field of visibleProperties(schema, property)) {
    const value = property[field.name];

    if (field.required && isEmptyValue(value)) {
      issues.push({
        level: 'error',
        target: { kind: 'node', id: node.id },
        message: `${node.id}: ${field.name} is required`,
      });
      continue;
    }
    if (field.enum && field.enum.length > 0 && !isEmptyValue(value)) {
      const text = String(value);
      if (!field.enum.includes(text)) {
        // An unrecognised value falls through to an unsupported-request 400,
        // or for If/Compare to a false result that routes down the wrong branch.
        issues.push({
          level: 'error',
          target: { kind: 'node', id: node.id },
          message: `${node.id}: ${field.name} = "${text}" is not one of ${field.enum.join(', ')}`,
        });
      }
    }
    if ((field.type === 'integer' || field.type === 'number') && typeof value === 'string') {
      issues.push({
        level: 'error',
        target: { kind: 'node', id: node.id },
        message: `${node.id}: ${field.name} must be a number`,
      });
    }
    if (field.type === 'object[]') {
      issues.push(...rowIssues(node, field, value));
    }
  }
  return issues;
}

/**
 * Rows of a repeatable property. An empty list satisfies the required check
 * above only when the property is absent, so a row added and then left blank
 * would otherwise pass and reach the engine as a condition that can never hold.
 */
function rowIssues(node: GraphNode, field: FieldSchema, value: unknown): ValidationIssue[] {
  const issues: ValidationIssue[] = [];
  const rows = toRowList(value);

  for (const [index, row] of rows.entries()) {
    for (const column of field.item ?? []) {
      if (!column.required) continue;
      if (isEmptyValue(row[column.name])) {
        issues.push({
          level: 'error',
          target: { kind: 'node', id: node.id },
          message: `${node.id}: ${field.name} row ${index + 1} needs ${column.name}`,
        });
      }
    }
  }
  return issues;
}

function unfedInputIssues(nodes: GraphNode[], edges: GraphEdge[]): ValidationIssue[] {
  const issues: ValidationIssue[] = [];

  const fed = new Set(edges.map((e) => `${e.target}:${parseHandleIndex(e.targetHandle)}`));
  for (const node of nodes) {
    // The node's own arity, not the schema's: a Module declares its ports per
    // instance, so the schema has nothing to say about it and the check would
    // silently skip exactly the node type most likely to be miswired.
    const ports = node.data.numInputPorts;
    for (let port = 0; port < ports; port++) {
      if (!fed.has(`${node.id}:${port}`)) {
        issues.push({
          level: 'warning',
          target: { kind: 'node', id: node.id },
          message: `${node.id} input port ${port} has no incoming edge; it will wait forever`,
        });
      }
    }
  }
  return issues;
}

export function countIssues(issues: ValidationIssue[]): { errors: number; warnings: number } {
  return {
    errors: issues.filter((i) => i.level === 'error').length,
    warnings: issues.filter((i) => i.level === 'warning').length,
  };
}
