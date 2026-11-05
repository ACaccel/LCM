import { useQueries, useQuery } from "@tanstack/react-query";
import { useCallback, useMemo, useState } from "react";
import { Link, useNavigate } from "react-router";
import { resourceQuery, resourcesQuery } from "../api/queries";
import type { Relation, ResourceDetail } from "../api/types";
import { ResourceGraph, TopologyLegend } from "../components/resource-graph";
import { Empty, ErrorBox, PageHead, Spinner } from "../components/ui";
import { buildTopology } from "../lib/topology";
import type { Topology } from "../lib/topology";
import { s } from "../strings";

// The resource list carries no relations, so the documents are read one by one.
// Keeping the combine function outside the component gives the result a stable identity.
const combineRelations = (results: { data: ResourceDetail | undefined; error: unknown; isPending: boolean }[]) => ({
  pending: results.some((r) => r.isPending),
  error: results.find((r) => r.error)?.error ?? null,
  relations: new Map<string, readonly Relation[]>(results.flatMap((r) => (r.data ? [[r.data.name, r.data.document.spec.relations ?? []] as const] : []))),
});

/** The edges of a topology as text, for readers who do not use the canvas. */
export function TopologyEdges({ topology }: { topology: Topology }) {
  if (topology.edges.length === 0) return null;
  return (
    <details>
      <summary>{s.topology.edgeList}</summary>
      <ul>
        {topology.edges.map((e) => (
          <li key={e.id}>
            {e.from} {s.relations.types[e.type] ?? e.type} {e.to}
          </li>
        ))}
      </ul>
    </details>
  );
}

/** Every resource with the relations its document declares; click one to open it. */
export function TopologyPage() {
  const navigate = useNavigate();
  const list = useQuery(resourcesQuery());
  const detail = useQueries({ queries: (list.data ?? []).map((r) => resourceQuery(r.name)), combine: combineRelations });
  const [onlyLinked, setOnlyLinked] = useState(true);
  const open = useCallback((name: string) => void navigate(`/resources/${encodeURIComponent(name)}`), [navigate]);
  const topology = useMemo(() => buildTopology(list.data ?? [], detail.relations, { onlyLinked }), [list.data, detail.relations, onlyLinked]);
  const waiting = list.isPending || (list.data !== undefined && list.data.length > 0 && detail.pending);
  return (
    <div className="page">
      <PageHead title={s.topology.title}>
        <Link to="/resources" className="btn">
          {s.topology.listView}
        </Link>
      </PageHead>
      <p className="meta">{s.topology.help}</p>
      <ErrorBox error={list.error ?? detail.error} onRetry={() => void list.refetch()} />
      {waiting ? (
        <Spinner label={s.topology.loading} />
      ) : list.data?.length === 0 ? (
        <Empty>{s.topology.noResources}</Empty>
      ) : (
        <div className="stack">
          <div className="row between">
            <label className="check">
              <input type="checkbox" checked={onlyLinked} onChange={(e) => setOnlyLinked(e.target.checked)} />
              {s.topology.onlyLinked}
            </label>
            <TopologyLegend />
          </div>
          {topology.nodes.length === 0 ? (
            <Empty>{s.topology.empty}</Empty>
          ) : (
            <>
              <ResourceGraph topology={topology} onOpen={open} />
              <TopologyEdges topology={topology} />
            </>
          )}
        </div>
      )}
    </div>
  );
}
