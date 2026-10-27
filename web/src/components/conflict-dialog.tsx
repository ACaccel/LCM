import { s } from "../strings";
import { Dialog } from "./dialog";
import { DiffView } from "./diff-view";
import { ErrorBox } from "./ui";

/**
 * Shown after a 409 version_conflict: the stored document moved on while
 * the user edited. The user either takes the stored version and drops the
 * edit, or saves the edit on top of the stored version.
 */
export function ConflictDialog({
  baseVersion,
  currentVersion,
  stored,
  mine,
  busy,
  error,
  onReload,
  onOverwrite,
  onClose,
}: {
  baseVersion: number;
  currentVersion: number | undefined;
  /** Text of the stored (current) version. */
  stored: string;
  /** Text the user wants to save. */
  mine: string;
  busy?: boolean;
  error?: unknown;
  onReload: () => void;
  onOverwrite: () => void;
  onClose: () => void;
}) {
  return (
    <Dialog
      title={s.config.conflictTitle}
      wide
      onClose={onClose}
      footer={
        <>
          <button type="button" className="btn" onClick={onClose}>
            {s.common.cancel}
          </button>
          <button type="button" className="btn" disabled={busy} onClick={onReload}>
            {s.config.conflictReload}
          </button>
          <button type="button" className="btn danger solid" disabled={busy || currentVersion === undefined} onClick={onOverwrite}>
            {s.config.conflictOverwrite}
          </button>
        </>
      }
    >
      <div className="stack">
        <div className="banner warn" role="alert">
          {currentVersion !== undefined ? s.config.conflictBody(currentVersion, baseVersion) : s.config.conflictTitle}
        </div>
        <div className="meta">{s.config.conflictDiff}</div>
        <DiffView before={stored} after={mine} />
        <ErrorBox error={error} />
      </div>
    </Dialog>
  );
}
