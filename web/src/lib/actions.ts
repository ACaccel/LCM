import type { ActionName, ServiceDetail } from "../api/types";
import { s } from "../strings";

/** Why an action button is disabled; empty when it is enabled. */
export function unavailableReason(detail: ServiceDetail, action: ActionName): string {
  const a = detail.actions.find((x) => x.action === action);
  if (!a || a.allowed) return "";
  if (a.reason === "invalidState") return s.action.unavailable.invalidState(s.state[detail.state] ?? detail.state);
  return a.reason === "unsupported" ? s.action.unavailable.unsupported : s.action.unavailable.notConfigured;
}
