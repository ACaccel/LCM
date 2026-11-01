import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { Link, NavLink, useParams,useNavigate } from "react-router";
import { client, unwrap } from "../api/client";
import { capabilitiesQuery, machineQuery, maintenanceQuery, mk } from "../api/machines";
import type { Machine } from "../api/machines";
import { ConfirmDialog } from "../components/dialog";
import { ErrorBox, PageHead, Spinner } from "../components/ui";
import { formatDateTime } from "../lib/format";
import { useLiveState } from "../live";
import { s } from "../strings";
import { MachineConfig, MachineVersions } from "./machine/config";
import { MaintenanceDialog } from "./machine/dialogs";
import { MachineEvents, MachineHealth, MachineHistory, MachineOverview } from "./machine/monitoring";
import { MachineControls, MachineOperations } from "./machine/operations";

function PhaseControls({ machine }: { machine: Machine }) {
	const navigate=useNavigate();
  const [action, setAction] = useState<"retire" | "resume" | "unmanage" | null>(null);
  const [baseVersion, setBaseVersion] = useState(machine.version);
  const qc = useQueryClient();
  const mutation = useMutation({
    mutationFn: (value: "retire" | "resume" | "unmanage") =>
      value === "unmanage"
        ? unwrap(client.DELETE("/machines/{machine}", { params: { path: { machine: machine.name }, header: { "If-Match": String(baseVersion) } } }))
        : unwrap(
            client.POST("/machines/{machine}/phase", {
              params: { path: { machine: machine.name }, header: { "If-Match": String(baseVersion) } },
              body: { phase: value === "retire" ? "retired" : "managed" },
            }),
          ),
    onSuccess: (_result,value) => {
      setAction(null);
      void qc.invalidateQueries({ queryKey: mk.all });
	  if(value==="unmanage")void navigate("/machines");
    },
  });
  return (
    <div className="row">
      {machine.phase !== "unmanaged" && (
        <>
          <button
            className="btn"
            onClick={() => {
              mutation.reset();
              setBaseVersion(machine.version);
              setAction(machine.phase === "retired" ? "resume" : "retire");
            }}
          >
            {machine.phase === "retired" ? s.machine.resume : s.machine.retire}
          </button>
          <button
            className="btn danger"
            onClick={() => {
              mutation.reset();
              setAction("unmanage");
              setBaseVersion(machine.version);
            }}
          >
            {s.machine.unmanage}
          </button>
        </>
      )}
      {action && (
        <ConfirmDialog
          title={s.machine[action]}
          body={
            <>
              <p>
                {s.machine.target}: {machine.name}
              </p>
              <p>{s.machine.phaseConfirm}</p>
            </>
          }
          danger={action === "unmanage"}
          busy={mutation.isPending}
          error={mutation.error}
          onClose={() => !mutation.isPending && setAction(null)}
          onConfirm={() => mutation.mutate(action)}
        />
      )}
    </div>
  );
}

function MachineDetail({ machine, tab, unavailable }: { machine: Machine; tab: string; unavailable: boolean }) {
	const selector=machine.phase==="unmanaged"?"@"+machine.id:machine.name;
  const caps = useQuery(capabilitiesQuery(selector));
  const maintenance = useQuery(maintenanceQuery(selector));
  const [dialog, setDialog] = useState<"enter" | "exit" | null>(null);
  const base = `/machines/${encodeURIComponent(selector)}`;
  const tabs = { overview: s.nav.overview, events: s.machine.events, history: s.machine.history, operations: s.machine.operations, config: s.config.title };
  return (
    <div className="stack">
      <PageHead
        title={machine.name}
        meta={
          <div className="row">
            <MachineHealth machine={machine} />
            <span>
              {s.machine.phase[machine.phase]} / {s.config.version(machine.version)}
            </span>
          </div>
        }
      >
        <Link className="btn" to="/machines">
          {s.common.back}
        </Link>
      </PageHead>
      <p className="meta">{s.machine.refreshNote}</p>
      <nav className="tabs" aria-label={s.common.details}>
        {Object.entries(tabs).map(([key, label]) => (
          <NavLink key={key} to={key === "overview" ? base : `${base}/${key}`} end>
            {label}
          </NavLink>
        ))}
      </nav>
      {tab === "overview" && <MachineOverview machine={machine} />}
      {tab === "events" && <MachineEvents name={selector} />}
      {tab === "history" && <MachineHistory name={selector} />}
      {tab === "config" && (
        <>
          <section className="panel">
            <MachineConfig machine={machine} />
          </section>
          <MachineVersions machine={machine} />
          <PhaseControls machine={machine} />
        </>
      )}
      {tab === "operations" && (
        <>
          <section className="panel stack">
            <h2>{s.machine.maintenance}</h2>
            <p className="meta">{s.machine.maintenanceNote}</p>
            <ErrorBox error={maintenance.error} onRetry={() => void maintenance.refetch()} />
            {maintenance.isPending && <Spinner />}
            {maintenance.data &&
              (maintenance.data.maintenance ? (
                <>
                  <strong>{s.machine.maintenanceActive}</strong>
                  <dl className="kv">
                    <dt>{s.common.actor}</dt>
                    <dd>{maintenance.data.maintenance.actor.name}</dd>
                    <dt>{s.machine.reason}</dt>
                    <dd>{maintenance.data.maintenance.reason}</dd>
                    <dt>{s.common.time}</dt>
                    <dd>{formatDateTime(maintenance.data.maintenance.enteredAt)}</dd>
                  </dl>
                </>
              ) : (
                <p>{s.machine.noMaintenance}</p>
              ))}
            <button
              className="btn"
              disabled={!maintenance.isSuccess || maintenance.isRefetchError || machine.phase !== "managed"}
              onClick={() => setDialog(maintenance.data?.maintenance ? "exit" : "enter")}
            >
              {maintenance.data?.maintenance ? s.machine.exit : s.machine.enter}
            </button>
          </section>
          <ErrorBox error={caps.error} onRetry={() => void caps.refetch()} />
          <MachineControls
            machine={machine}
            caps={caps.isSuccess ? caps.data.items : undefined}
            maintenance={maintenance.data?.maintenance}
            unavailable={unavailable || caps.isError || maintenance.isError || maintenance.isPending}
          />
          <MachineOperations name={selector} />
        </>
      )}
      {dialog && <MaintenanceDialog machine={machine} exit={dialog === "exit"} onClose={() => setDialog(null)} />}
    </div>
  );
}

export function MachineDetailPage() {
  const { name = "", tab = "overview" } = useParams();
  const q = useQuery(machineQuery(name));
  const live = useLiveState();
  const qc = useQueryClient();
  useEffect(() => {
    if (live === "open") void qc.invalidateQueries({ queryKey: mk.detail(name) });
  }, [live, name, qc]);
  return (
    <>
      <ErrorBox error={q.error} onRetry={() => void q.refetch()} />
      {q.isPending && <Spinner />}
      {q.data && (
        <MachineDetail
          key={q.data.id}
          machine={q.data}
          unavailable={q.isError}
          tab={["overview", "events", "history", "operations", "config"].includes(tab) ? tab : "overview"}
        />
      )}
    </>
  );
}
