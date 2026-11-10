import type { ResourceDetail, ServiceRoute } from "../../api/types";
import { Pill } from "../../components/ui";
import { formatDateTime } from "../../lib/format";
import { routeStateView } from "../../lib/status";
import { s } from "../../strings";

/** The locations of a route section, as lines of text relative to the route's prefix. */
export function routeLines(route: ServiceRoute): { group: string; lines: string[] }[] {
  const out: { group: string; lines: string[] }[] = [];
  const proxy = (route.proxy ?? []).map((p) => {
    const parts = [s.route.location(p.path, p.upstream)];
    if (p.exact) parts.push(s.route.exact);
    if (p.bodyLimit) parts.push(s.route.bodyLimit(p.bodyLimit));
    if (p.cookiePath) parts.push(s.route.cookiePath);
    if (p.websocket) parts.push(s.route.websocket);
    if (p.timeout) parts.push(s.route.timeout(p.timeout));
    if (p.category) parts.push(s.route.category(p.category));
    return parts.join(" · ");
  });
  if (proxy.length > 0) out.push({ group: s.route.proxy, lines: proxy });
  const st = route.static;
  if (st) {
    const lines: string[] = [];
    if (st.spa) lines.push(s.route.spa);
    if (st.notFound) lines.push(s.route.notFound(st.notFound));
    if (st.immutable && st.immutable.length > 0) lines.push(s.route.immutable(st.immutable.join(", ")));
    for (const d of st.downloads ?? []) lines.push(s.route.download(d.path, d.type));
    if (lines.length > 0) out.push({ group: s.route.static, lines });
  }
  const redirects = (route.redirects ?? []).map((r) => s.route.redirect(r.from, r.to, r.code ?? 301));
  if (redirects.length > 0) out.push({ group: s.route.redirects, lines: redirects });
  if (route.headers?.csp) out.push({ group: s.route.headers, lines: [`${s.route.csp(route.headers.cspReportOnly ?? false)}: ${route.headers.csp}`] });
  return out;
}

/**
 * Read-only summary of a service's route section and the last result of
 * sending it to the host. Editing happens in the document; the host decides
 * whether the route is granted, so a refusal is shown here, not on save.
 */
export function RoutePanel({ detail }: { detail: ResourceDetail }) {
  const status = detail.service?.route;
  if (!status) return null;
  const route = detail.document.spec.config?.route;
  const groups = route ? routeLines(route) : [];
  return (
    <section className="panel stack">
      <h2>{s.route.title}</h2>
      <p className="meta">{s.route.help}</p>
      <dl className="kv">
        <dt>{s.route.result}</dt>
        <dd>
          {status.state ? <Pill view={routeStateView(status.state)} title={status.message} /> : s.route.notSent}
          {status.at && <span className="meta"> · {formatDateTime(status.at)}</span>}
          {status.state && !status.current && <div className="meta">{s.route.stale}</div>}
          {status.message && <div className="meta">{status.message}</div>}
        </dd>
        {status.siteSha256 && (
          <>
            <dt>{s.route.site}</dt>
            <dd>
              <code>{status.siteSha256}</code>
            </dd>
          </>
        )}
        {status.changes.length > 0 && (
          <>
            <dt>{s.route.changes}</dt>
            <dd>{status.changes.join("; ")}</dd>
          </>
        )}
        {status.dropped.length > 0 && (
          <>
            <dt>{s.route.dropped}</dt>
            <dd>
              <ul className="plain-list">
                {status.dropped.map((d) => (
                  <li key={d} className="meta">
                    {d}
                  </li>
                ))}
              </ul>
            </dd>
          </>
        )}
      </dl>
      {route ? (
        groups.length > 0 ? (
          groups.map((g) => (
            <div key={g.group} className="stack">
              <h3>{g.group}</h3>
              <ul className="plain-list">
                {g.lines.map((l) => (
                  <li key={l} className="list-item">
                    {l}
                  </li>
                ))}
              </ul>
            </div>
          ))
        ) : (
          <p className="meta">{s.route.defaults}</p>
        )
      ) : null}
      {status.state !== "applied" && status.state !== "" && <p className="meta">{s.route.retry}</p>}
    </section>
  );
}
