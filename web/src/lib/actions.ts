import type { ResourceAction, ResourceDetail } from "../api/types";
import { s } from "../strings";
import { translate } from "./messages";

/** Service reason codes have their own wording; other types send a message with a code to translate. */
export function reasonText(a: Pick<ResourceAction, "reason" | "reasonMessage">, state: string): string {
  switch (a.reason) {
    case "":
      return "";
    case "invalidState":
      return s.action.unavailable.invalidState(s.state[state] ?? state);
    case "unsupported":
      return s.action.unavailable.unsupported;
    case "notConfigured":
      return s.action.unavailable.notConfigured;
    case "notManaged":
      return s.action.unavailable.notManaged;
    case "busy":
      return s.action.unavailable.busy;
    default:
      return translate(a.reasonMessage);
  }
}

/** Why an action button is disabled; empty when it is enabled or the resource does not offer it. */
export function unavailableReason(detail: Pick<ResourceDetail, "actions" | "state">, name: string): string {
  const a = detail.actions.find((x) => x.name === name);
  if (!a) return s.action.unavailable.notOffered;
  if (a.available) return "";
  return reasonText(a, detail.state) || s.action.unavailable.notOffered;
}

export function actionLabel(name: string): string {
  return s.actionName[name] ?? name;
}
