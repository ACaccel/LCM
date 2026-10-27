import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";
import { client, unwrap, yamlBody } from "../api/client";
import { qk, servicesQuery } from "../api/queries";
import type { Health, ServiceState, ServiceSummary } from "../api/types";
import { HealthPill, ManagedBadges, StatePill } from "../components/badges";
import { Dialog } from "../components/dialog";
import { useToast } from "../components/toast";
import { Async, Empty, ErrorBox, PageHead, Tag } from "../components/ui";
import { ValidationReport } from "../components/validation";
import { YamlEditor } from "../components/yaml-editor-lazy";
import { shortVersion } from "../lib/format";
import { serviceTemplate } from "../lib/templates";
import { s } from "../strings";

const states: ServiceState[] = ["registered", "deploying", "running", "updating", "rolling_back", "stopped", "failed", "retired"];
const healths: Health[] = ["healthy", "degraded", "down", "unknown"];

function ServiceRow({ svc }: { svc: ServiceSummary }) {
  return (
    <tr>
      <td>
        <Link to={`/services/${encodeURIComponent(svc.name)}`}>{svc.name}</Link>
      </td>
      <td>
        <StatePill state={svc.state} />
      </td>
      <td>
        <HealthPill health={svc.health} />
      </td>
      <td>
        <code>{shortVersion(svc.deployedVersion)}</code>
      </td>
      <td>{svc.host}</td>
      <td>
        <div className="row" style={{ gap: 4 }}>
          <ManagedBadges managedBy={svc.managedBy} />
        </div>
      </td>
      <td>
        <div className="row" style={{ gap: 4 }}>
          {Object.entries(svc.labels).map(([k, v]) => (
            <Tag key={k}>{`${k}=${v}`}</Tag>
          ))}
        </div>
      </td>
    </tr>
  );
}

function CreateDialog({ onClose }: { onClose: () => void }) {
  const [text, setText] = useState(serviceTemplate);
  const qc = useQueryClient();
  const navigate = useNavigate();
  const toast = useToast();

  const validate = useMutation({
    mutationFn: () => unwrap(client.POST("/validate", { body: text as never, ...yamlBody })),
  });
  const create = useMutation({
    mutationFn: () => unwrap(client.POST("/services", { body: text as never, ...yamlBody })),
    onSuccess: (doc) => {
      void qc.invalidateQueries({ queryKey: qk.servicesAll() });
      toast.success(s.services.created);
      onClose();
      void navigate(`/services/${encodeURIComponent(doc.document.metadata.name)}`);
    },
  });

  return (
    <Dialog
      title={s.services.createTitle}
      wide
      onClose={onClose}
      footer={
        <>
          <button type="button" className="btn" onClick={onClose}>
            {s.common.cancel}
          </button>
          <button type="button" className="btn" disabled={validate.isPending} onClick={() => validate.mutate()}>
            {s.services.validate}
          </button>
          <button type="button" className="btn primary" disabled={create.isPending} onClick={() => create.mutate()}>
            {s.common.create}
          </button>
        </>
      }
    >
      <div className="stack">
        <p className="meta">{s.services.createHelp}</p>
        <YamlEditor value={text} onChange={setText} ariaLabel={s.common.serviceYaml} />
        {validate.data && <ValidationReport {...validate.data} />}
        <ErrorBox error={validate.error ?? create.error} />
      </div>
    </Dialog>
  );
}

export function ServicesPage() {
  const [params, setParams] = useSearchParams();
  const label = params.get("label") ?? "";
  const state = (params.get("state") ?? "") as ServiceState | "";
  const health = (params.get("health") ?? "") as Health | "";
  const creating = params.get("create") === "1";

  const labels = label
    .split(",")
    .map((x) => x.trim())
    .filter(Boolean);
  const q = useQuery(servicesQuery({ label: labels, state: state || undefined, health: health || undefined }));

  function set(key: string, value: string) {
    const next = new URLSearchParams(params);
    if (value) next.set(key, value);
    else next.delete(key);
    setParams(next, { replace: true });
  }

  return (
    <div className="page">
      <PageHead title={s.services.title}>
        <button type="button" className="btn primary" onClick={() => set("create", "1")}>
          {s.services.create}
        </button>
      </PageHead>
      <div className="filters">
        <div className="field">
          <label htmlFor="f-label">{s.services.filterLabel}</label>
          <input id="f-label" type="search" value={label} placeholder="tier=web" onChange={(e) => set("label", e.target.value)} />
        </div>
        <div className="field">
          <label htmlFor="f-state">{s.services.filterState}</label>
          <select id="f-state" value={state} onChange={(e) => set("state", e.target.value)}>
            <option value="">{s.common.all}</option>
            {states.map((x) => (
              <option key={x} value={x}>
                {s.state[x]}
              </option>
            ))}
          </select>
        </div>
        <div className="field">
          <label htmlFor="f-health">{s.services.filterHealth}</label>
          <select id="f-health" value={health} onChange={(e) => set("health", e.target.value)}>
            <option value="">{s.common.all}</option>
            {healths.map((x) => (
              <option key={x} value={x}>
                {s.health[x]}
              </option>
            ))}
          </select>
        </div>
      </div>
      <Async q={q}>
        {(items) =>
          items.length === 0 ? (
            <Empty>{s.services.empty}</Empty>
          ) : (
            <div className="table-wrap">
              <table className="table">
                <thead>
                  <tr>
                    <th>{s.services.columns.name}</th>
                    <th>{s.services.columns.state}</th>
                    <th>{s.services.columns.health}</th>
                    <th>{s.services.columns.version}</th>
                    <th>{s.services.columns.host}</th>
                    <th>{s.services.columns.managed}</th>
                    <th>{s.services.columns.labels}</th>
                  </tr>
                </thead>
                <tbody>
                  {items.map((svc) => (
                    <ServiceRow key={svc.name} svc={svc} />
                  ))}
                </tbody>
              </table>
            </div>
          )
        }
      </Async>
      {creating && <CreateDialog onClose={() => set("create", "")} />}
    </div>
  );
}
