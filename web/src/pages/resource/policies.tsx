import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Link } from "react-router";
import { policiesQuery } from "../../api/queries";
import type { ResourceDetail } from "../../api/types";
import { ManagedBadges } from "../../components/badges";
import { Async, Empty } from "../../components/ui";
import { actionLabel } from "../../lib/actions";
import { policyRelation } from "../../lib/policy-match";
import type { PolicyRelation } from "../../lib/policy-match";
import { s } from "../../strings";
import { TemplateDialog } from "../template-dialog";

function relationText(r: PolicyRelation): string {
  if (r.kind === "dynamic") return s.resourcePolicies.relation.dynamic;
  if (r.kind === "watches") return s.resourcePolicies.relation.watches;
  return s.resourcePolicies.relation.acts(r.types.map((t) => (t.startsWith("lifecycle.") ? actionLabel(t.slice("lifecycle.".length)) : t)).join("、"));
}

/** Deployed policies that act on the resource or listen to it. Templates target services only. */
export function PoliciesTab({ detail }: { detail: ResourceDetail }) {
  const q = useQuery(policiesQuery());
  const [applying, setApplying] = useState(false);
  return (
    <Async q={q}>
      {(policies) => {
        const ref = { name: detail.name, type: detail.type, labels: detail.labels };
        const matched = policies.flatMap((p) => {
          const relation = policyRelation(p, ref);
          return relation ? [{ policy: p, relation }] : [];
        });
        return (
          <div className="stack">
            <div className="row">
              <p className="meta grow">{s.resourcePolicies.help}</p>
              {detail.type === "service" && detail.phase === "managed" && (
                <button type="button" className="btn primary" onClick={() => setApplying(true)}>
                  {s.resourcePolicies.applyTemplate}
                </button>
              )}
            </div>
            {applying && <TemplateDialog resource={detail.name} onClose={() => setApplying(false)} />}
            {matched.length === 0 ? (
              <Empty>{s.resourcePolicies.empty}</Empty>
            ) : (
              <div className="table-wrap">
                <table className="table">
                  <thead>
                    <tr>
                      <th>{s.resourcePolicies.columns.name}</th>
                      <th>{s.resourcePolicies.columns.relation}</th>
                      <th>{s.resourcePolicies.columns.state}</th>
                      <th>{s.resourcePolicies.columns.managed}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {matched.map(({ policy: p, relation }) => (
                      <tr key={p.name}>
                        <td>
                          <Link to={`/policies/${encodeURIComponent(p.name)}`}>{p.name}</Link>
                        </td>
                        <td>{relationText(relation)}</td>
                        <td>{p.paused ? s.policies.paused : s.policies.deployed}</td>
                        <td>
                          <div className="row" style={{ gap: 4 }}>
                            <ManagedBadges managedBy={p.managedBy} />
                          </div>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        );
      }}
    </Async>
  );
}
