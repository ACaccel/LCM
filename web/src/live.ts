import { createContext, useContext } from "react";
import type { StreamState } from "./api/events";

/** State of the tab's event stream, for features that fall back to polling. */
export const LiveContext = createContext<StreamState>("connecting");

export function useLiveState(): StreamState {
  return useContext(LiveContext);
}
