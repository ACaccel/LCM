import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { impactQuery } from "../../api/queries";
import type { ResourceImpact } from "../../api/types";
import { ErrorBox, Spinner } from "../../components/ui";
import { s } from "../../strings";

/** Acknowledgement state of one confirmation: the server decides whether it is required. */
export interface ImpactAck {
  impact: ResourceImpact | undefined;
  /** The server requires acknowledgeImpact for this action and impact. */
  required: boolean;
  checked: boolean;
  setChecked: (v: boolean) => void;
  /** The confirmation may be sent: the impact is known and acknowledged where required. */
  ready: boolean;
  /** Value of acknowledgeImpact in the request. */
  acknowledged: boolean;
  loading: boolean;
  error: unknown;
}

/** Combines a server-computed impact with the operator's checkbox. */
export function useAck(impact: ResourceImpact | undefined, loading = false, error: unknown = null): ImpactAck {
  const [checked, setChecked] = useState(false);
  const required = !!impact?.acknowledgementRequired;
  return {
    impact,
    required,
    checked,
    setChecked,
    ready: !!impact && (!required || checked),
    acknowledged: required && checked,
    loading,
    error,
  };
}

/** The impact of an action on a resource, fetched while a confirmation is open. */
export function useImpactAck(resource: string, action: string, enabled = true): ImpactAck {
  const q = useQuery({ ...impactQuery(resource, action), enabled: enabled && !!action, staleTime: 0 });
  return useAck(q.data, q.isPending && enabled, q.error);
}

/** Who an action reaches, and the explicit acknowledgement the server requires for it. */
export function ImpactAcknowledgement({ state }: { state: ImpactAck }) {
  if (state.loading) return <Spinner label={s.impact.loading} />;
  if (state.error) return <ErrorBox error={new Error(s.impact.unavailable)} />;
  const imp = state.impact;
  if (!imp || !state.required) return null;
  return (
    <div className="banner warn stack" role="group" aria-label={s.impact.title}>
      <strong>{s.impact.title}</strong>
      <p>{s.impact.intro}</p>
      <ul>
        {imp.dependents.map((d) => (
          <li key={`d/${d.name}`}>{s.impact.dependent(d.name, s.relations.types[d.relation] ?? d.relation, d.via)}</li>
        ))}
        {imp.controlDependents.map((d) => (
          <li key={`c/${d.name}`}>{s.impact.control(d.name, d.via)}</li>
        ))}
      </ul>
      <label className="check">
        <input type="checkbox" checked={state.checked} onChange={(e) => state.setChecked(e.target.checked)} />
        {s.impact.ack}
      </label>
    </div>
  );
}
