/**
 * lcmApi.ts
 *
 * Typed REST client for the LCM Enabler backend.
 * All requests go to /api (proxied to :8000 by Vite in development).
 * Errors surface as thrown Error objects with contextual messages.
 */

import type {
  ModuleConfig,
  ServiceConfig,
  PolicyConfig,
  PolicyStatusResponse,
  NodeTypesResponse,
  DeployRequest,
  WebhookRequest,
  WebhookResponse,
  TemplateRequest,
  RunTrace,
  UploadServiceConfigResponse,
} from '../model/types';

// ─── Helpers ──────────────────────────────────────────────────────────────────

async function handleResponse<T>(res: Response, operation: string): Promise<T> {
  if (!res.ok) {
    let detail = res.statusText;
    try {
      const body = await res.json() as { error?: string; msg?: string };
      detail = body.error ?? body.msg ?? detail;
    } catch {
      // Response body is not JSON; use status text.
    }
    throw new Error(`${operation} failed (HTTP ${res.status}): ${detail}`);
  }
  // Some endpoints return null on success.
  const text = await res.text();
  if (!text.trim()) return null as unknown as T;
  return JSON.parse(text) as T;
}

function jsonPost(url: string, body: unknown): Promise<Response> {
  return fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
}

function jsonPut(url: string, body: unknown): Promise<Response> {
  return fetch(url, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
}

// ─── Service ──────────────────────────────────────────────────────────────────

export async function listServices(): Promise<string[]> {
  const res = await fetch('/api/lcm/service');
  return handleResponse<string[]>(res, 'listServices');
}

export async function getService(name: string): Promise<ServiceConfig> {
  const res = await fetch(`/api/lcm/service/${encodeURIComponent(name)}`);
  return handleResponse<ServiceConfig>(res, `getService(${name})`);
}

export async function createService(config: ServiceConfig): Promise<{ msg: string }> {
  const res = await jsonPost('/api/lcm/service', config);
  return handleResponse<{ msg: string }>(res, 'createService');
}

export async function updateService(config: ServiceConfig): Promise<{ msg: string }> {
  const res = await jsonPut('/api/lcm/service', config);
  return handleResponse<{ msg: string }>(res, 'updateService');
}

export async function deleteService(name: string): Promise<void> {
  const res = await fetch(`/api/lcm/service/${encodeURIComponent(name)}`, {
    method: 'DELETE',
  });
  await handleResponse<unknown>(res, `deleteService(${name})`);
}

/**
 * Uploads a hand-written service config YAML file.
 * 'create' registers a new service (POST); 'reconfigure' replaces an existing
 * service's whole config (PUT — refused while the service has deployed
 * policies). No Content-Type header: the browser must set the multipart
 * boundary itself.
 */
export async function uploadServiceConfig(
  file: File,
  mode: 'create' | 'reconfigure',
): Promise<UploadServiceConfigResponse> {
  const form = new FormData();
  form.append('file', file);
  const res = await fetch('/api/lcm/service/file', {
    method: mode === 'create' ? 'POST' : 'PUT',
    body: form,
  });
  return handleResponse<UploadServiceConfigResponse>(
    res,
    `uploadServiceConfig(${file.name})`,
  );
}

// ─── Node Types ───────────────────────────────────────────────────────────────

export async function getNodeTypes(): Promise<NodeTypesResponse> {
  const res = await fetch('/api/lcm/nodetypes');
  return handleResponse<NodeTypesResponse>(res, 'getNodeTypes');
}

// ─── Policy ───────────────────────────────────────────────────────────────────

export async function getPolicy(
  svcName: string,
  policyName: string,
): Promise<PolicyStatusResponse> {
  const res = await fetch(
    `/api/lcm/service/${encodeURIComponent(svcName)}/policy/${encodeURIComponent(policyName)}`,
  );
  return handleResponse<PolicyStatusResponse>(res, `getPolicy(${svcName}/${policyName})`);
}

export async function createPolicy(
  svcName: string,
  policy: PolicyConfig,
): Promise<{ msg: string }> {
  const res = await jsonPost(
    `/api/lcm/service/${encodeURIComponent(svcName)}/policy`,
    policy,
  );
  return handleResponse<{ msg: string }>(res, `createPolicy(${svcName})`);
}

export async function updatePolicy(
  svcName: string,
  policyName: string,
  policy: PolicyConfig,
): Promise<{ msg: string }> {
  const res = await jsonPut(
    `/api/lcm/service/${encodeURIComponent(svcName)}/policy/${encodeURIComponent(policyName)}`,
    policy,
  );
  return handleResponse<{ msg: string }>(res, `updatePolicy(${svcName}/${policyName})`);
}

export async function deletePolicy(svcName: string, policyName: string): Promise<void> {
  const res = await fetch(
    `/api/lcm/service/${encodeURIComponent(svcName)}/policy/${encodeURIComponent(policyName)}`,
    { method: 'DELETE' },
  );
  await handleResponse<unknown>(res, `deletePolicy(${svcName}/${policyName})`);
}

// ─── Deploy / Undeploy ────────────────────────────────────────────────────────

export async function deployPolicies(req: DeployRequest): Promise<void> {
  const res = await jsonPost('/api/lcm/service/policy/deploy', req);
  await handleResponse<null>(res, 'deployPolicies');
}

export async function undeployPolicies(req: DeployRequest): Promise<void> {
  const res = await jsonPost('/api/lcm/service/policy/undeploy', req);
  await handleResponse<null>(res, 'undeployPolicies');
}

// ─── Webhook ──────────────────────────────────────────────────────────────────

export async function triggerWebhook(req: WebhookRequest): Promise<WebhookResponse> {
  const res = await jsonPost('/api/lcm/webhook', req);
  return handleResponse<WebhookResponse>(res, 'triggerWebhook');
}

// ─── Template ─────────────────────────────────────────────────────────────────

export async function createTemplate(req: TemplateRequest): Promise<{ msg: string }> {
  const res = await jsonPost('/api/lcm/service/template', req);
  return handleResponse<{ msg: string }>(res, 'createTemplate');
}

/**
 * Fetches the latest execution trace of a deployed policy.
 * Pass the highest sequence number already held as `since` to fetch only newer
 * events; reset to 0 whenever the returned runId changes.
 */
export async function getPolicyRun(
  svcName: string,
  policyName: string,
  since = 0,
): Promise<RunTrace> {
  const url =
    `/api/lcm/service/${encodeURIComponent(svcName)}/policy/${encodeURIComponent(policyName)}` +
    `/run?since=${since}`;
  const res = await fetch(url);
  return handleResponse<RunTrace>(res, `getPolicyRun(${svcName}/${policyName})`);
}

// ─── Module ───────────────────────────────────────────────────────────────────

export async function listModules(svcName: string): Promise<string[]> {
  const res = await fetch(`/api/lcm/service/${encodeURIComponent(svcName)}/module`);
  return handleResponse<string[]>(res, `listModules(${svcName})`);
}

export async function getModule(svcName: string, moduleName: string): Promise<ModuleConfig> {
  const res = await fetch(
    `/api/lcm/service/${encodeURIComponent(svcName)}/module/${encodeURIComponent(moduleName)}`,
  );
  return handleResponse<ModuleConfig>(res, `getModule(${svcName}/${moduleName})`);
}

export async function createModule(
  svcName: string,
  module: ModuleConfig,
): Promise<{ msg: string }> {
  const res = await jsonPost(`/api/lcm/service/${encodeURIComponent(svcName)}/module`, module);
  return handleResponse<{ msg: string }>(res, `createModule(${svcName})`);
}

export async function updateModule(
  svcName: string,
  moduleName: string,
  module: ModuleConfig,
): Promise<{ msg: string }> {
  const res = await jsonPut(
    `/api/lcm/service/${encodeURIComponent(svcName)}/module/${encodeURIComponent(moduleName)}`,
    module,
  );
  return handleResponse<{ msg: string }>(res, `updateModule(${svcName}/${moduleName})`);
}

export async function deleteModule(svcName: string, moduleName: string): Promise<void> {
  const res = await fetch(
    `/api/lcm/service/${encodeURIComponent(svcName)}/module/${encodeURIComponent(moduleName)}`,
    { method: 'DELETE' },
  );
  await handleResponse<unknown>(res, `deleteModule(${svcName}/${moduleName})`);
}
