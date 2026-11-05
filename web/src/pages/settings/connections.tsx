import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router";
import { connectionsQuery } from "../../api/queries";
import { Async, Empty } from "../../components/ui";
import { s } from "../../strings";

/** Host-configured connections, read only: they change only in the host's connections file. */
export function ConnectionsPanel() {
  const q = useQuery(connectionsQuery());
  return (
    <div className="stack">
      <p className="meta">{s.settings.connections.help}</p>
      <Async q={q}>
        {(items) =>
          items.length === 0 ? (
            <Empty>{s.settings.connections.empty}</Empty>
          ) : (
            <div className="table-wrap">
              <table className="table">
                <thead>
                  <tr>
                    <th>{s.settings.connections.name}</th>
                    <th>{s.settings.connections.type}</th>
                    <th>{s.settings.connections.allowActions}</th>
                    <th>{s.settings.connections.allowResources}</th>
                    <th>{s.settings.connections.resources}</th>
                  </tr>
                </thead>
                <tbody>
                  {items.map((c) => (
                    <tr key={c.name}>
                      <td>
                        <code>{c.name}</code>
                      </td>
                      <td>{c.type}</td>
                      <td>{c.allowActions.join(", ") || s.settings.connections.noActions}</td>
                      <td>{c.allowResources.join(", ") || s.settings.connections.noResources}</td>
                      <td>
                        {c.resources.length === 0
                          ? s.common.none
                          : c.resources.map((r, i) => (
                              <span key={r}>
                                {i > 0 && ", "}
                                <Link to={`/resources/${encodeURIComponent(r)}`}>{r}</Link>
                              </span>
                            ))}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )
        }
      </Async>
    </div>
  );
}
