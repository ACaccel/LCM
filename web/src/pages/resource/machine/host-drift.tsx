import { useQuery } from "@tanstack/react-query";
import { hostComponentsQuery } from "../../../api/queries";
import type { HostComponent, HostFile } from "../../../api/types";
import { s } from "../../../strings";

// The operations guide owns the update procedure.
const updateGuide = "https://github.com/ACaccel/lcm/blob/main/docs/operations.md#rerunning-checking-and-updates";

/**
 * Files in the given states, with the component they belong to. A component
 * that is not installed is a choice, so its files (all missing) never count.
 */
function files(items: HostComponent[], states: string[]): { component: string; file: HostFile }[] {
  return items
    .filter((c) => c.state !== "not_installed")
    .flatMap((c) => c.files.filter((f) => states.includes(f.state)).map((file) => ({ component: c.name, file })));
}

function FileList({ rows }: { rows: { component: string; file: HostFile }[] }) {
  return (
    <ul>
      {rows.map(({ component, file }) => (
        <li key={file.path}>
          <code>{file.path}</code> ({component}) · {s.hostDrift.states[file.state] ?? file.state}
          {file.detail ? `：${file.detail}` : ""}
        </li>
      ))}
    </ul>
  );
}

/**
 * Tells the administrator that the root-owned helper files of the host no
 * longer match this LCM build. It shows nothing when everything matches, a
 * component is not installed, or a file is unreadable: sudoers rules and the
 * security host helper are root-only by design, so an unreadable file is the
 * normal state, not a finding. A failed or empty read shows nothing either:
 * the notice must never be the reason a page looks broken.
 */
export function HostDriftNotice() {
  const q = useQuery(hostComponentsQuery());
  if (!q.data) return null;
  const drift = files(q.data, ["outdated", "missing"]);
  if (drift.length === 0) return null;
  return (
    <div className="banner warn stack" role="status">
      <strong>{s.hostDrift.title}</strong>
      <FileList rows={drift} />
      <p>{s.hostDrift.help}</p>
      <pre className="block">{s.hostDrift.command}</pre>
      <p className="meta">{s.hostDrift.warning}</p>
      <a href={updateGuide} target="_blank" rel="noreferrer">
        {s.hostDrift.docs}
      </a>
    </div>
  );
}
