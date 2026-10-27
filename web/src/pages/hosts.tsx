import { useQuery } from "@tanstack/react-query";
import { hostsQuery, servicesQuery } from "../api/queries";
import { Async, Empty, PageHead, Tag } from "../components/ui";
import { s } from "../strings";

export function HostsPage() {
  const hosts = useQuery(hostsQuery());
  const services = useQuery(servicesQuery());
  const count = (host: string) => services.data?.filter((x) => x.host === host).length;
  return (
    <div className="page">
      <PageHead title={s.hosts.title} />
      <Async q={hosts}>
        {(items) =>
          items.length === 0 ? (
            <Empty>{s.hosts.empty}</Empty>
          ) : (
            <div className="table-wrap">
              <table className="table">
                <thead>
                  <tr>
                    <th>{s.common.name}</th>
                    <th>{s.hosts.kind}</th>
                    <th>{s.hosts.address}</th>
                    <th>{s.hosts.services}</th>
                  </tr>
                </thead>
                <tbody>
                  {items.map((h) => (
                    <tr key={h.name}>
                      <td>{h.name}</td>
                      <td>
                        <Tag>{h.kind}</Tag>
                      </td>
                      <td>
                        <code>{h.address || "-"}</code>
                      </td>
                      <td className="num">{count(h.name) ?? "-"}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )
        }
      </Async>
      <p className="meta">{s.hosts.note}</p>
    </div>
  );
}
