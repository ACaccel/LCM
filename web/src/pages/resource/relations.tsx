import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router";
import { impactQuery, resourcesQuery } from "../../api/queries";
import type { ImpactEntry, ResourceDetail } from "../../api/types";
import { TypeTag } from "../../components/badges";
import { Async, Empty, ErrorBox, Pill } from "../../components/ui";
import { s } from "../../strings";

function ResourceLink({ name }: { name: string }) {
  return <Link to={`/resources/${encodeURIComponent(name)}`}>{name}</Link>;
}

function AffectedTable({ entries, empty }: { entries: ImpactEntry[]; empty: string }) {
  if (!entries.length) return <Empty>{empty}</Empty>;
  return (
    <div className="table-wrap">
      <table className="table">
        <thead>
          <tr>
            <th>{s.relations.target}</th>
            <th>{s.resources.columns.type}</th>
            <th>{s.relations.type}</th>
            <th>{s.relations.via}</th>
            <th>{s.relations.depth}</th>
          </tr>
        </thead>
        <tbody>
          {entries.map((e) => (
            <tr key={e.name}>
              <td>
                <ResourceLink name={e.name} />
              </td>
              <td>{s.resourceType[e.type]}</td>
              <td>{s.relations.types[e.relation] ?? e.relation}</td>
              <td>
                <ResourceLink name={e.via} />
              </td>
              <td>{e.depth}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/**
 * Relations the document declares, and the managed resources that depend on
 * this one or are controlled through it, as the server computes them.
 */
export function RelationsTab({ detail }: { detail: ResourceDetail }) {
  const relations = detail.document.spec.relations ?? [];
  const resources = useQuery({ ...resourcesQuery(), enabled: relations.length > 0 });
  const impact = useQuery({ ...impactQuery(detail.name), enabled: detail.phase !== "unmanaged" });
  const byName = new Map((resources.data ?? []).map((r) => [r.name, r]));
  // The server says which targets exist; the list only adds links and types.
  const unresolved = new Set(detail.unresolvedRelations);
  return (
    <div className="stack">
      <section className="panel stack">
        <h2>{s.relations.outgoing}</h2>
        <p className="meta">{s.relations.help}</p>
        {relations.length === 0 ? (
          <Empty>{s.relations.empty}</Empty>
        ) : (
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th>{s.relations.type}</th>
                  <th>{s.relations.target}</th>
                  <th>{s.resources.columns.type}</th>
                </tr>
              </thead>
              <tbody>
                {relations.map((r) => {
                  const target = byName.get(r.target);
                  return (
                    <tr key={`${r.type}/${r.target}`}>
                      <td>{s.relations.types[r.type] ?? r.type}</td>
                      <td>{target ? <ResourceLink name={r.target} /> : r.target}</td>
                      <td>
                        {unresolved.has(r.target) ? (
                          <Pill view={{ tone: "warn", icon: "▲", label: s.relations.unresolved }} />
                        ) : (
                          target && <TypeTag type={target.type} adapter={target.adapter} />
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
        <ErrorBox error={resources.error} onRetry={() => void resources.refetch()} />
      </section>
      {detail.phase !== "unmanaged" && (
        <Async q={impact}>
          {(imp) => (
            <>
              <section className="panel stack">
                <h2>{s.relations.dependents}</h2>
                <p className="meta">{s.relations.dependentsHelp}</p>
                <AffectedTable entries={imp.dependents} empty={s.relations.noDependents} />
              </section>
              <section className="panel stack">
                <h2>{s.relations.controlDependents}</h2>
                <p className="meta">{s.relations.controlDependentsHelp}</p>
                <AffectedTable entries={imp.controlDependents} empty={s.relations.noControlDependents} />
              </section>
              <p className="meta">{s.relations.retiredNote}</p>
            </>
          )}
        </Async>
      )}
    </div>
  );
}
