import { useQuery } from "@tanstack/react-query";
import { backupsQuery } from "../../api/queries";
import type { Backup, BackupSource } from "../../api/types";
import { Async, Empty, Pill, Tag } from "../../components/ui";
import { formatBytes, formatDateTime, formatTime } from "../../lib/format";
import { backupOutcomeView } from "../../lib/status";
import { s } from "../../strings";

/** A source as the operator wrote it: its type and path, database or program. */
export function describeSource(src: BackupSource): string {
  switch (src.type) {
    case "files":
    case "sqlite":
      return `${src.type} ${src.path ?? ""}`;
    case "postgres":
    case "mongodb":
      return `${src.type} ${src.database ?? ""}${src.users ? " + users" : ""}`;
    default:
      return `${src.type} ${(src.argv ?? []).join(" ")}`;
  }
}

/** Whether a restore puts a source back: command output is only kept. */
export function restorable(src: BackupSource): boolean {
  return src.type !== "command";
}

/** Whether a set holds the users and roles of a MongoDB instance, which a restore may put back. */
export function holdsUsers(sources: BackupSource[]): boolean {
  return sources.some((src) => src.type === "mongodb" && !!src.users);
}

/** Backup sets, pre-restore copies and restores of a service, with restore from a set. */
export function BackupsTab({ resource, canRestore, onRestore }: { resource: string; canRestore: boolean; onRestore: (backupId: string) => void }) {
  const q = useQuery(backupsQuery(resource));
  return (
    <Async q={q}>
      {(data) => {
        const items: Backup[] = data.items;
        if (items.length === 0) {
          return (
            <div className="stack">
              <Empty>{s.backups.empty}</Empty>
              <p className="meta">{s.backups.emptyHint}</p>
            </div>
          );
        }
        return (
          <div className="stack">
            <p className="meta">{s.backups.help}</p>
            <div className="table-wrap">
              <table className="table">
                <thead>
                  <tr>
                    <th>{s.backups.columns.backup}</th>
                    <th>{s.deployments.started}</th>
                    <th>{s.backups.columns.sources}</th>
                    <th>{s.backups.columns.size}</th>
                    <th>{s.backups.columns.outcome}</th>
                    <th>{s.common.actor}</th>
                    <th>{s.common.actions}</th>
                  </tr>
                </thead>
                <tbody>
                  {items.map((b) => {
                    const enabled = canRestore && b.restorable;
                    return (
                      <tr key={b.id}>
                        <td>
                          <Tag>{s.backups.kinds[b.kind]}</Tag>
                          {b.backupId && (
                            <div>
                              <code>{b.backupId}</code>
                            </div>
                          )}
                          {b.prunedAt && <div className="meta">{s.backups.removed}</div>}
                        </td>
                        <td title={formatDateTime(b.startedAt)}>{formatTime(b.startedAt)}</td>
                        <td>
                          {b.sources.length === 0 ? (
                            "-"
                          ) : (
                            <ul className="plain-list">
                              {b.sources.map((src, i) => (
                                <li key={i}>
                                  {describeSource(src)}
                                  {!restorable(src) && <span className="meta"> ({s.backups.notRestored})</span>}
                                </li>
                              ))}
                            </ul>
                          )}
                          {b.skipped > 0 && <div className="meta">{s.backups.skipped(b.skipped)}</div>}
                        </td>
                        <td>{b.kind === "restore" ? "-" : formatBytes(b.size)}</td>
                        <td>
                          <Pill view={backupOutcomeView(b.outcome)} />
                          {b.error && <div className="meta">{b.error}</div>}
                          {b.manifestSha256 && (
                            <details>
                              <summary className="meta">{s.backups.manifest}</summary>
                              <code>{b.manifestSha256}</code>
                            </details>
                          )}
                        </td>
                        <td>{b.actor.name}</td>
                        <td>
                          {(b.kind === "backup" || b.kind === "pre-restore") && (
                            <button
                              type="button"
                              className="btn small"
                              disabled={!enabled}
                              title={enabled ? s.backups.restoreOf(b.backupId) : b.restorable ? s.backups.restoreUnavailable : s.backups.restoreDisabled}
                              onClick={() => onRestore(b.backupId)}
                            >
                              {s.backups.restoreTo}
                            </button>
                          )}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          </div>
        );
      }}
    </Async>
  );
}
