import { useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { Link, useNavigate } from "react-router";
import { machinesQuery, mk } from "../api/machines";
import { Dialog } from "../components/dialog";
import { ErrorBox, PageHead, Spinner } from "../components/ui";
import { useLiveState } from "../live";
import { s } from "../strings";
import { MachineConfig } from "./machine/config";
import { MachineHealth } from "./machine/monitoring";

export function MachinesPage() {
	const [archived,setArchived]=useState(false);
  const q = useInfiniteQuery(machinesQuery(archived));
  const [create, setCreate] = useState(false);
  const navigate = useNavigate();
  const live = useLiveState();
  const qc = useQueryClient();
  useEffect(() => {
    if (live === "open") void qc.invalidateQueries({ queryKey: mk.all });
  }, [live, qc]);
  const machines = q.data?.pages.flatMap((p) => p.items) ?? [];
  return (
    <div className="stack">
      <PageHead title={s.machine.title} meta={<p className="meta">{s.machine.refreshNote}</p>}>
        <button className="btn primary" onClick={() => setCreate(true)}>
          {s.machine.register}
        </button>
        <button className="btn" onClick={() => void q.refetch()}>
          {s.common.refresh}
        </button>
      </PageHead>
	  <label className="check"><input type="checkbox" checked={archived} onChange={e=>setArchived(e.target.checked)}/>{s.machine.archives}</label>
      <ErrorBox error={q.error} onRetry={() => void q.refetch()} />
      {q.isPending && <Spinner />}
      {!q.isPending && !machines.length && <p className="empty">{s.machine.empty}</p>}
      <div className="panel table-wrap">
        <table className="table">
          <thead>
            <tr>
              <th>{s.common.name}</th>
              <th>{s.machine.status}</th>
              <th>{s.common.status}</th>
              <th>{s.machine.identity}</th>
              <th>{s.common.version}</th>
            </tr>
          </thead>
          <tbody>
            {machines.map((machine) => (
              <tr key={machine.id}>
                <td>
                  <Link to={`/machines/${encodeURIComponent(archived?"@"+machine.id:machine.name)}`}>{machine.name}</Link>
                </td>
                <td>{s.machine.phase[machine.phase]}</td>
                <td>
                  <MachineHealth machine={machine} />
                </td>
                <td>{s.machine.identityStates[machine.status.identity] ?? machine.status.identity}</td>
                <td>{machine.version}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {q.hasNextPage && (
        <button className="btn" disabled={q.isFetchingNextPage} onClick={() => void q.fetchNextPage()}>
          {s.common.more}
        </button>
      )}
      {create && (
        <Dialog title={s.machine.register} wide onClose={() => setCreate(false)}>
          <MachineConfig
            onCreated={(machine) => {
              setCreate(false);
              void navigate(`/machines/${encodeURIComponent(machine.name)}`);
            }}
          />
        </Dialog>
      )}
    </div>
  );
}
