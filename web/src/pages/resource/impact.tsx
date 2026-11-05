import { useQuery } from "@tanstack/react-query";
import { impactQuery } from "../../api/queries";
import type { ResourceImpact } from "../../api/types";
import { ErrorBox, Spinner } from "../../components/ui";
import { s } from "../../strings";

/**
 * Acknowledgement state of one confirmation: the server decides whether it is
 * required. Pressing the confirm button is the acknowledgement; there is no
 * separate checkbox, so the button states how many resources it affects.
 */
export interface ImpactAck {
  impact: ResourceImpact | undefined;
  /** The server requires acknowledgeImpact for this action and impact. */
  required: boolean;
  /** Other resources the action reaches; 0 unless an acknowledgement is required. */
  count: number;
  /** The confirmation may be sent: the impact is known. */
  ready: boolean;
  /** Value of acknowledgeImpact in the request. */
  acknowledged: boolean;
  loading: boolean;
  error: unknown;
}

/** The distinct resources an impact reaches, whichever way. */
export function impactCount(impact: Pick<ResourceImpact, "dependents" | "controlDependents">): number {
  return new Set([...impact.dependents, ...impact.controlDependents].map((d) => d.name)).size;
}

/** Combines a server-computed impact with the rule that confirming acknowledges it. */
export function useAck(impact: ResourceImpact | undefined, loading = false, error: unknown = null): ImpactAck {
  const required = !!impact?.acknowledgementRequired;
  return {
    impact,
    required,
    count: required && impact ? impactCount(impact) : 0,
    ready: !!impact,
    acknowledged: required,
    loading,
    error,
  };
}

/** The impact of an action on a resource, fetched while a confirmation is open. */
export function useImpactAck(resource: string, action: string, enabled = true): ImpactAck {
  const q = useQuery({ ...impactQuery(resource, action), enabled: enabled && !!action, staleTime: 0 });
  return useAck(q.data, q.isPending && enabled, q.error);
}

/**
 * The label of a confirm button: it names the verb and the number of
 * resources affected when the impact must be acknowledged, otherwise it is
 * the plain label.
 */
export function confirmLabel(plain: string, verb: string, ack: Pick<ImpactAck, "required" | "count">): string {
  return ack.required && ack.count > 0 ? s.impact.confirm(verb, ack.count) : plain;
}

/** The resources an impact reaches, as list items. */
export function ImpactList({ impact }: { impact: Pick<ResourceImpact, "dependents" | "controlDependents"> }) {
  return (
    <ul>
      {impact.dependents.map((d) => (
        <li key={`d/${d.name}`}>{s.impact.dependent(d.name, s.relations.types[d.relation] ?? d.relation, d.via)}</li>
      ))}
      {impact.controlDependents.map((d) => (
        <li key={`c/${d.name}`}>{s.impact.control(d.name, d.via)}</li>
      ))}
    </ul>
  );
}

/** Who an action reaches. Confirming the dialog acknowledges it. */
export function ImpactAcknowledgement({ state }: { state: ImpactAck }) {
  if (state.loading) return <Spinner label={s.impact.loading} />;
  if (state.error) return <ErrorBox error={new Error(s.impact.unavailable)} />;
  const imp = state.impact;
  if (!imp || !state.required) return null;
  return (
    <div className="banner warn stack" role="group" aria-label={s.impact.title}>
      <strong>{s.impact.title}</strong>
      <p>{s.impact.intro}</p>
      <ImpactList impact={imp} />
      <p className="meta">{s.impact.confirmNote}</p>
    </div>
  );
}
