import { useInfiniteQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { parseDocument, stringify } from "yaml";
import { client, isApiError, unwrap } from "../../api/client";
import { machineVersionsQuery, mk } from "../../api/machines";
import type { Machine, MachineDocumentInput } from "../../api/machines";
import { ConfirmDialog } from "../../components/dialog";
import { ErrorBox } from "../../components/ui";
import { formatDateTime } from "../../lib/format";
import { s } from "../../strings";

export const MACHINE_YAML_LIMIT = 65_536;
const initial: MachineDocumentInput = { apiVersion: "lcm/v1", kind: "Machine", metadata: { name: "local-machine" }, spec: { host: "local" } };

export function parseMachineYaml(text: string, name?: string): MachineDocumentInput {
  if (new TextEncoder().encode(text).length > MACHINE_YAML_LIMIT) throw new Error(s.machine.tooLarge);
  const doc = parseDocument(text);
  if (doc.errors.length) throw new Error(s.machine.invalidYaml);
  let data: unknown;
  try {
    data = doc.toJS({ maxAliasCount: 50 });
  } catch {
    throw new Error(s.machine.invalidYaml);
  }
  if (!data || typeof data !== "object") throw new Error(s.machine.localRequired);
  const value = data as MachineDocumentInput;
  if (
    value.apiVersion !== "lcm/v1" ||
    value.kind !== "Machine" ||
    typeof value.metadata?.name !== "string" ||
    !value.metadata.name ||
    !value.spec ||
    (value.spec.host !== undefined && value.spec.host !== "local") ||
    (name && value.metadata.name !== name)
  )
    throw new Error(s.machine.localRequired);
  return value;
}

export function MachineConfig({ machine, onCreated }: { machine?: Machine; onCreated?: (machine: Machine) => void }) {
  const [text, setText] = useState(() => stringify(machine?.document ?? initial));
  const [version, setVersion] = useState(machine?.version ?? 0);
  const [saved, setSaved] = useState("");
  const qc = useQueryClient();
  const save = useMutation({
    mutationFn: async () => {
      const body = parseMachineYaml(text, machine?.name);
      return machine
        ? unwrap(client.PUT("/machines/{machine}", { params: { path: { machine: machine.name }, header: { "If-Match": String(version) } }, body }))
        : unwrap(client.POST("/machines", { body }));
    },
    onSuccess: (result) => {
      setVersion(result.version);
      setText(stringify(result.document));
      setSaved(s.config.saved);
      void qc.invalidateQueries({ queryKey: mk.all });
      onCreated?.(result);
    },
  });
  return (
    <div className="stack">
      <p className="meta">{s.machine.localOnly}</p>
      <label className="field">
        <span>{s.machine.config}</span>
        <textarea
          rows={20}
          maxLength={MACHINE_YAML_LIMIT}
          value={text}
		  readOnly={machine?.phase==="unmanaged"}
          onChange={(e) => {
            setText(e.target.value);
            setSaved("");
          }}
          spellCheck={false}
        />
      </label>
      <p className="meta">{s.machine.bounded}</p>
      {version > 0 && <span className="meta">{s.config.version(version)}</span>}
      {isApiError(save.error) && save.error.isConflict && <div className="banner warn">{s.machine.conflict}</div>}
      <ErrorBox error={save.error} />
      <div className="row">
        <button className="btn primary" disabled={save.isPending||machine?.phase==="unmanaged"} onClick={() => save.mutate()}>
          {machine ? s.config.saveVersion : s.machine.register}
        </button>
        {machine && (
          <button
            className="btn"
            disabled={save.isPending}
            onClick={() => {
              setText(stringify(machine.document));
              setVersion(machine.version);
              save.reset();
              setSaved("");
            }}
          >
            {s.common.reload}
          </button>
        )}
        {saved && <span role="status">{saved}</span>}
      </div>
    </div>
  );
}

export function MachineVersions({ machine }: { machine: Machine }) {
  const q = useInfiniteQuery(machineVersionsQuery(machine.phase==="unmanaged"?"@"+machine.id:machine.name));
  const [restore, setRestore] = useState<{ version: number; base: number } | null>(null);
  const qc = useQueryClient();
  const mutation = useMutation({
    mutationFn: (v: { version: number; base: number }) =>
      unwrap(
        client.POST("/machines/{machine}/restore", {
          params: { path: { machine: machine.name }, header: { "If-Match": String(v.base) } },
          body: { version: v.version },
        }),
      ),
    onSuccess: () => {
      setRestore(null);
      void qc.invalidateQueries({ queryKey: mk.all });
    },
  });
  return (
    <section className="panel stack">
      <h2>{s.config.history}</h2>
      <ErrorBox error={q.error} onRetry={() => void q.refetch()} />
      {q.data?.pages
        .flatMap((p) => p.items)
        .map((v) => (
          <details className="panel" key={v.version}>
            <summary>
              {s.config.version(v.version)} — {formatDateTime(v.createdAt)} — {v.createdBy.name}
            </summary>
            {v.restoredFrom > 0 && <p className="meta">{s.config.restoredFrom(v.restoredFrom)}</p>}
            <pre className="block">{stringify(v.document)}</pre>
            <button
              className="btn"
              disabled={machine.phase !== "managed"}
              onClick={() => {
                mutation.reset();
                setRestore({ version: v.version, base: machine.version });
              }}
            >
              {s.config.restore}
            </button>
          </details>
        ))}
      {q.hasNextPage && (
        <button className="btn" disabled={q.isFetchingNextPage} onClick={() => void q.fetchNextPage()}>
          {s.common.more}
        </button>
      )}
      {restore && (
        <ConfirmDialog
          title={s.config.restore}
          body={
            <>
              <p>{s.config.confirmRestore(restore.version)}</p>
              {isApiError(mutation.error) && mutation.error.isConflict && <p>{s.machine.conflict}</p>}
            </>
          }
          busy={mutation.isPending}
          error={mutation.error}
          onClose={() => !mutation.isPending && setRestore(null)}
          onConfirm={() => mutation.mutate(restore)}
        />
      )}
    </section>
  );
}
