import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { client, unwrap } from "../../../api/client";
import { qk, resourcesQuery } from "../../../api/queries";
import type { ResourceDetail } from "../../../api/types";
import { Dialog } from "../../../components/dialog";
import { ErrorBox, Spinner } from "../../../components/ui";
import { actionLabel } from "../../../lib/actions";
import { s } from "../../../strings";

/** Known services, which all run on the local machine; coverage stays partial. */
export function WorkloadDisclosure() {
  const services = useQuery(resourcesQuery({ type: "service" }));
  return (
    <section className="stack">
      <h3>{s.machine.workloads}</h3>
      <div className="banner warn">{s.machine.partial}</div>
      {services.isPending && <Spinner />}
      {services.isError && <p>{s.machine.workloadUnknown}</p>}
      <ErrorBox error={services.error} />
      {services.data && (
        <ul>
          {services.data
            .filter((svc) => svc.phase === "managed")
            .map((svc) => (
              <li key={svc.name}>
                <code>{svc.name}</code> — {s.state[svc.state] ?? svc.state}
              </li>
            ))}
        </ul>
      )}
    </section>
  );
}

export function Acknowledgements({
  workload,
  recovery,
  onWorkload,
  onRecovery,
}: {
  workload: boolean;
  recovery: boolean;
  onWorkload: (v: boolean) => void;
  onRecovery: (v: boolean) => void;
}) {
  return (
    <>
      <label className="check">
        <input type="checkbox" checked={workload} onChange={(e) => onWorkload(e.target.checked)} />
        {s.machine.workloadAck}
      </label>
      <label className="check">
        <input type="checkbox" checked={recovery} onChange={(e) => onRecovery(e.target.checked)} />
        {s.machine.recoveryAck}
      </label>
    </>
  );
}

export function MaintenanceDialog({ machine, exit, onClose }: { machine: Pick<ResourceDetail, "name" | "version">; exit: boolean; onClose: () => void }) {
  const preview = useQuery({
    queryKey: [...qk.resourceMaintenance(machine.name), "preview"],
    queryFn: () => unwrap(client.GET("/resources/{resource}/maintenance/preview", { params: { path: { resource: machine.name } } })),
    enabled: !exit,
    staleTime: 0,
  });
  const [baseVersion] = useState(machine.version);
  const [reason, setReason] = useState("");
  const [workload, setWorkload] = useState(false);
  const [recovery, setRecovery] = useState(false);
  const qc = useQueryClient();
  const mutation = useMutation({
    mutationFn: () =>
      exit
        ? unwrap(
            client.POST("/resources/{resource}/maintenance/exit", {
              params: { path: { resource: machine.name }, header: { "If-Match": String(baseVersion) } },
              body: { reason: reason.trim() },
            }),
          )
        : unwrap(
            client.POST("/resources/{resource}/maintenance", {
              params: { path: { resource: machine.name }, header: { "If-Match": String(baseVersion) } },
              body: { reason: reason.trim(), acknowledged: workload && recovery, previewHash: preview.data?.hash ?? "" },
            }),
          ),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.resourceAll(machine.name) });
      onClose();
    },
  });
  return (
    <Dialog
      title={actionLabel(exit ? "maintenance-exit" : "maintenance-enter")}
      onClose={() => !mutation.isPending && onClose()}
      footer={
        <>
          <button type="button" className="btn" disabled={mutation.isPending} onClick={onClose}>
            {s.common.cancel}
          </button>
          <button
            type="button"
            className="btn primary"
            disabled={mutation.isPending || !reason.trim() || !workload || !recovery || (!exit && !preview.data)}
            onClick={() => mutation.mutate()}
          >
            {s.common.confirm}
          </button>
        </>
      }
    >
      <div className="stack">
        <p>
          {s.operation.target}: <strong>{machine.name}</strong>
        </p>
        <p>{s.machine.maintenanceNote}</p>
        <WorkloadDisclosure />
        {!exit && (
          <>
            <ErrorBox error={preview.error} />
            {preview.isPending && <Spinner />}
            {preview.data && (
              <ul>
                {preview.data.workloads.map((w) => (
                  <li key={w.name}>
                    <code>{w.name}</code> — {s.state[w.state] ?? w.state} (v{w.version})
                  </li>
                ))}
              </ul>
            )}
          </>
        )}
        <div className="field">
          <label htmlFor="maintenance-reason">{s.machine.reason}</label>
          <textarea id="maintenance-reason" maxLength={1000} value={reason} onChange={(e) => setReason(e.target.value)} />
          <span className="hint">{s.machine.reasonHint}</span>
        </div>
        <Acknowledgements workload={workload} recovery={recovery} onWorkload={setWorkload} onRecovery={setRecovery} />
        <ErrorBox error={mutation.error} />
      </div>
    </Dialog>
  );
}
