import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { client, unwrap } from "../../api/client";
import type { Machine } from "../../api/machines";
import { mk } from "../../api/machines";
import { servicesQuery } from "../../api/queries";
import { Dialog } from "../../components/dialog";
import { ErrorBox, Spinner } from "../../components/ui";
import { s } from "../../strings";

export function WorkloadDisclosure() {
  const services = useQuery(servicesQuery());
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
            .filter((svc) => svc.host === "local")
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

export function MaintenanceDialog({ machine, exit, onClose }: { machine: Machine; exit: boolean; onClose: () => void }) {
	const preview=useQuery({queryKey:[...mk.detail(machine.name),"preview"],queryFn:()=>unwrap(client.GET("/machines/{machine}/maintenance/preview",{params:{path:{machine:machine.name}}})),enabled:!exit,staleTime:0});
  const [baseVersion] = useState(machine.version);
  const [reason, setReason] = useState("");
  const [workload, setWorkload] = useState(false);
  const [recovery, setRecovery] = useState(false);
  const qc = useQueryClient();
  const mutation = useMutation({
    mutationFn: () =>
      exit
        ? unwrap(client.POST("/machines/{machine}/maintenance/exit", { params: { path: { machine: machine.name }, header: { "If-Match": String(baseVersion) } }, body: { reason: reason.trim() } }))
        : unwrap(
            client.POST("/machines/{machine}/maintenance", {
              params: { path: { machine: machine.name }, header: { "If-Match": String(baseVersion) } },
              body: { reason: reason.trim(), acknowledged: workload && recovery, previewHash: preview.data?.hash ?? "" },
            }),
          ),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: mk.all });
      onClose();
    },
  });
  return (
    <Dialog
      title={exit ? s.machine.exit : s.machine.enter}
      onClose={() => !mutation.isPending && onClose()}
      footer={
        <>
          <button className="btn" disabled={mutation.isPending} onClick={onClose}>
            {s.common.cancel}
          </button>
          <button className="btn primary" disabled={mutation.isPending || !reason.trim() || !workload || !recovery || (!exit&&!preview.data)} onClick={() => mutation.mutate()}>
            {s.common.confirm}
          </button>
        </>
      }
    >
      <div className="stack">
        <p>
          {s.machine.target}: <strong>{machine.name}</strong>
        </p>
        <p>{s.machine.maintenanceNote}</p>
        <WorkloadDisclosure />
		{!exit&&<><ErrorBox error={preview.error}/>{preview.isPending&&<Spinner/>}{preview.data&&<ul>{preview.data.workloads.map(w=><li key={w.name}><code>{w.name}</code> — {s.state[w.state]??w.state} (v{w.version})</li>)}</ul>}</>}
        <label className="field">
          <span>{s.machine.reason}</span>
          <textarea maxLength={1000} value={reason} onChange={(e) => setReason(e.target.value)} />
          <span className="hint">{s.machine.reasonHint}</span>
        </label>
        <Acknowledgements workload={workload} recovery={recovery} onWorkload={setWorkload} onRecovery={setRecovery} />
        <ErrorBox error={mutation.error} />
      </div>
    </Dialog>
  );
}

export function ResolveDialog({ name, id, version, onClose }: { name: string; id: string; version: number; onClose: () => void }) {
  const [reason, setReason] = useState("");
  const [ack, setAck] = useState(false);
  const qc = useQueryClient();
  const mutation = useMutation({
    mutationFn: () =>
      unwrap(client.POST("/machines/{machine}/operations/{id}/resolve", { params: { path: { machine: name, id }, header: { "If-Match": String(version) } }, body: { reason: reason.trim() } })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: mk.all });
      onClose();
    },
  });
  return (
    <Dialog
      title={s.machine.resolve}
      onClose={() => !mutation.isPending && onClose()}
      footer={
        <>
          <button className="btn" disabled={mutation.isPending} onClick={onClose}>
            {s.common.cancel}
          </button>
          <button className="btn danger" disabled={mutation.isPending || !reason.trim() || !ack} onClick={() => mutation.mutate()}>
            {s.machine.resolve}
          </button>
        </>
      }
    >
      <div className="stack">
        <p>
          {s.machine.target}: {name} / <code>{id}</code>
        </p>
        <div className="banner warn">{s.machine.resolveNote}</div>
        <label className="field">
          <span>{s.machine.reason}</span>
          <textarea maxLength={1000} value={reason} onChange={(e) => setReason(e.target.value)} />
        </label>
        <label className="check">
          <input type="checkbox" checked={ack} onChange={(e) => setAck(e.target.checked)} />
          {s.machine.recoveryAck}
        </label>
        <ErrorBox error={mutation.error} />
      </div>
    </Dialog>
  );
}
