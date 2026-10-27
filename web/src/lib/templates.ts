export const serviceTemplate = `apiVersion: lcm/v1
kind: Service
metadata:
  name: my-service
  labels:
    tier: app
spec:
  host: local
  runtime:
    type: systemd
    unit: my-service.service
    scope: user
  checks:
    - name: http
      type: http
      interval: 30s
      props:
        url: http://127.0.0.1:8080/health
`;

export function policyTemplate(name: string): string {
  return `apiVersion: lcm/v1
kind: Policy
metadata:
  name: ${name}
spec:
  nodes:
    - id: start
      type: trigger.manual
`;
}
