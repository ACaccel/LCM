import { useMutation } from "@tanstack/react-query";
import { useState } from "react";
import { Link } from "react-router";
import { client, unwrap } from "../../api/client";
import type { RunMode } from "../../api/types";
import { Dialog } from "../../components/dialog";
import { ErrorBox } from "../../components/ui";
import { s } from "../../strings";

/** Starts a run of the deployed policy by hand, live or as a dry run. */
export function ManualRunDialog({ policy, triggers, deployed, onClose }: { policy: string; triggers: readonly string[]; deployed: boolean; onClose: () => void }) {
  const [mode, setMode] = useState<RunMode>("dry");
  const [trigger, setTrigger] = useState("");
  const [event, setEvent] = useState("{}");
  const [jsonError, setJsonError] = useState(false);

  const m = useMutation({
    mutationFn: () => {
      let parsed: unknown;
      try {
        parsed = event.trim() ? JSON.parse(event) : undefined;
      } catch {
        setJsonError(true);
        throw new Error(s.editor.invalidJson);
      }
      setJsonError(false);
      return unwrap(
        client.POST("/policies/{p}/runs", {
          params: { path: { p: policy } },
          body: { mode, ...(trigger ? { trigger } : {}), ...(parsed === undefined ? {} : { event: parsed }) },
        }),
      );
    },
  });

  return (
    <Dialog
      title={s.editor.runTitle}
      onClose={onClose}
      footer={
        <>
          <button type="button" className="btn" onClick={onClose}>
            {s.common.close}
          </button>
          <button type="button" className="btn primary" disabled={!deployed || m.isPending} onClick={() => m.mutate()}>
            {s.editor.runStart}
          </button>
        </>
      }
    >
      <div className="stack">
        {!deployed && <div className="banner warn">{s.editor.runNotDeployed}</div>}
        <fieldset style={{ border: 0, padding: 0, margin: 0 }}>
          <legend className="meta">{s.editor.runMode}</legend>
          <div className="row">
            {(["dry", "live"] as const).map((v) => (
              <label key={v} className="check">
                <input type="radio" name="run-mode" checked={mode === v} onChange={() => setMode(v)} />
                {s.runMode[v]}
              </label>
            ))}
          </div>
          {mode === "live" && <p className="meta">{s.editor.liveWarn}</p>}
        </fieldset>
        <div className="field">
          <label htmlFor="run-trigger">{s.editor.runTrigger}</label>
          <select id="run-trigger" value={trigger} onChange={(e) => setTrigger(e.target.value)}>
            <option value="">{s.editor.runAuto}</option>
            {triggers.map((t) => (
              <option key={t} value={t}>
                {t}
              </option>
            ))}
          </select>
        </div>
        <div className="field">
          <label htmlFor="run-event">{s.editor.runEvent}</label>
          <textarea id="run-event" rows={5} value={event} aria-invalid={jsonError || undefined} spellCheck={false} onChange={(e) => setEvent(e.target.value)} />
        </div>
        <ErrorBox error={m.error} />
        {m.data && (
          <div className="banner ok" role="status">
            {s.editor.runResult}:{" "}
            <Link to={`/runs/${encodeURIComponent(m.data.runId)}`} onClick={onClose}>
              {s.editor.runOpen} <code>{m.data.runId.slice(0, 8)}</code>
            </Link>
          </div>
        )}
      </div>
    </Dialog>
  );
}
