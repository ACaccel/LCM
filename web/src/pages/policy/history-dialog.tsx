import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { policyVersionQuery, policyVersionsQuery } from "../../api/queries";
import { Dialog } from "../../components/dialog";
import { DiffView } from "../../components/diff-view";
import { Async, ErrorBox, Spinner } from "../../components/ui";
import { formatTime } from "../../lib/format";
import { docToYaml } from "../../lib/yamldoc";
import { s } from "../../strings";

function VersionDiff({ name, version, current, onLoad }: { name: string; version: number; current: string; onLoad: (text: string) => void }) {
  const q = useQuery(policyVersionQuery(name, version));
  if (q.isPending) return <Spinner />;
  if (q.isError) return <ErrorBox error={q.error} />;
  const text = docToYaml(q.data.document);
  return (
    <div className="stack">
      <div className="row between">
        <h3 style={{ margin: 0 }}>
          {s.config.version(version)} → {s.editor.versionNow}
        </h3>
        <button type="button" className="btn primary small" onClick={() => onLoad(text)}>
          {s.editor.loadVersion}
        </button>
      </div>
      <DiffView before={text} after={current} />
    </div>
  );
}

/**
 * Version history of a policy. `current` is the editor's content in
 * docToYaml form, so that the diff shows content changes and not
 * differences of YAML style.
 */
export function HistoryDialog({ name, deployedVersion, current, onLoad, onClose }: { name: string; deployedVersion: number | null; current: string; onLoad: (text: string, version: number) => void; onClose: () => void }) {
  const q = useQuery(policyVersionsQuery(name));
  const [selected, setSelected] = useState<number | null>(null);
  return (
    <Dialog title={s.editor.history} wide onClose={onClose} footer={<button type="button" className="btn primary" onClick={onClose}>{s.common.close}</button>}>
      <div className="stack">
        <Async q={q}>
          {(data) => (
            <ul style={{ listStyle: "none", margin: 0, padding: 0 }}>
              {data.items.map((v) => (
                <li key={v.version} className="row between" style={{ padding: "4px 0", borderBottom: "1px solid var(--border)" }}>
                  <span>
                    <b>{s.config.version(v.version)}</b>{" "}
                    {v.version === deployedVersion && <span className="pill ok">{s.editor.stateDeployed}</span>}{" "}
                    <span className="meta">
                      {formatTime(v.createdAt)} · {v.createdBy.name}
                    </span>
                  </span>
                  <button type="button" className="btn small" aria-pressed={selected === v.version} onClick={() => setSelected(selected === v.version ? null : v.version)}>
                    {s.editor.compare}
                  </button>
                </li>
              ))}
            </ul>
          )}
        </Async>
        {selected !== null && <VersionDiff name={name} version={selected} current={current} onLoad={(text) => onLoad(text, selected)} />}
      </div>
    </Dialog>
  );
}
