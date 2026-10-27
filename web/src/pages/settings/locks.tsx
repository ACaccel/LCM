import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { client, unwrap } from "../../api/client";
import { locksQuery, qk } from "../../api/queries";
import type { LockInfo } from "../../api/types";
import { ConfirmDialog } from "../../components/dialog";
import { Async, Empty } from "../../components/ui";
import { formatDateTime } from "../../lib/format";
import { s } from "../../strings";

export function LocksPanel() {
  const q = useQuery(locksQuery());
  const qc = useQueryClient();
  const [target, setTarget] = useState<LockInfo | null>(null);
  const release = useMutation({
    // Lock names contain slashes; openapi-fetch percent-encodes path params.
    mutationFn: (name: string) => unwrap(client.DELETE("/locks/{name}", { params: { path: { name } } })),
    onSuccess: () => {
      setTarget(null);
      void qc.invalidateQueries({ queryKey: qk.locks() });
    },
  });
  return (
    <div className="stack">
      <p className="meta">{s.settings.locks.help}</p>
      <Async q={q}>
        {(items) =>
          items.length === 0 ? (
            <Empty>{s.settings.locks.empty}</Empty>
          ) : (
            <div className="table-wrap">
              <table className="table">
                <thead>
                  <tr>
                    <th>{s.settings.locks.name}</th>
                    <th>{s.settings.locks.owner}</th>
                    <th>{s.settings.locks.acquired}</th>
                    <th>{s.settings.locks.expires}</th>
                    <th>{s.common.actions}</th>
                  </tr>
                </thead>
                <tbody>
                  {items.map((l) => (
                    <tr key={l.name}>
                      <td>
                        <code>{l.name}</code>
                      </td>
                      <td>
                        <code>{l.owner}</code>
                      </td>
                      <td>{formatDateTime(l.acquiredAt)}</td>
                      <td>{formatDateTime(l.expiresAt)}</td>
                      <td>
                        <button type="button" className="btn small danger" onClick={() => setTarget(l)}>
                          {s.settings.locks.release}
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )
        }
      </Async>
      {target && (
        <ConfirmDialog
          title={s.settings.locks.release}
          confirmLabel={s.settings.locks.release}
          body={s.settings.locks.confirmRelease(target.name)}
          danger
          busy={release.isPending}
          error={release.error}
          onClose={() => setTarget(null)}
          onConfirm={() => release.mutate(target.name)}
        />
      )}
    </div>
  );
}
