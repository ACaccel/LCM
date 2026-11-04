export function policyTemplate(name: string): string {
  return `apiVersion: lcm/v2
kind: Policy
metadata:
  name: ${name}
spec:
  nodes:
    - id: start
      type: trigger.manual
`;
}

/**
 * The starting document of a new resource. It only carries what the chosen
 * type descriptor says (type, adapter, whether a connection is named); the
 * config fields and their rules stay with the server, which validates them.
 */
export function resourceSkeleton(type: string, adapter: string, needsConnection: boolean): string {
  return `apiVersion: lcm/v2
kind: Resource
metadata:
  name: my-${type}
  labels: {}
spec:
  type: ${type}
  adapter: ${adapter}
${needsConnection ? "  connection: \"\"\n" : ""}  config: {}
  checks: []
  relations: []
`;
}
