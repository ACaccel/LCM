import { useQuery } from "@tanstack/react-query";
import { useCallback, useMemo, useState } from "react";
import { Link, useNavigate } from "react-router";
import { resourcesQuery } from "../api/queries";
import { ResourceGraph, TopologyLegend } from "../components/resource-graph";
import { Empty, ErrorBox, PageHead, Spinner } from "../components/ui";
import { buildTopology } from "../lib/topology";
import type { Topology } from "../lib/topology";
import { s } from "../strings";

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
  // The list summaries carry the relations, so the whole page is one request.
  const list = useQuery(resourcesQuery());
  const [onlyLinked, setOnlyLinked] = useState(true);
  const open = useCallback((name: string) => void navigate(`/resources/${encodeURIComponent(name)}`), [navigate]);
  const topology = useMemo(() => {
    const resources = list.data ?? [];
    return buildTopology(resources, new Map(resources.map((r) => [r.name, r.relations])), { onlyLinked });
  }, [list.data, onlyLinked]);
  return (
    <div className="page">
      <PageHead title={s.topology.title}>
        <Link to="/resources" className="btn">
          {s.topology.listView}
        </Link>
      </PageHead>
      <p className="meta">{s.topology.help}</p>
      <ErrorBox error={list.error} onRetry={() => void list.refetch()} />
      {list.isPending ? (
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
