/**
 * useStore.ts
 *
 * Global application state managed with Zustand.
 * Keeps UI state (selected service/policy, toast messages) in one place
 * so sibling components can react without prop-drilling.
 */

import { create } from 'zustand';
import type { PolicyStatus } from '../model/types';

export type Toast = {
  id: number;
  message: string;
  kind: 'success' | 'error' | 'info';
};

type AppState = {
  // Currently open service name (null = none selected).
  selectedService: string | null;
  // Currently open policy name (null = none selected).
  selectedPolicy: string | null;
  /**
   * Currently open module name (null = none).
   *
   * A module and a policy are never open at once, and module mode deliberately
   * leaves selectedPolicy null: that is what makes the Inspector's policy
   * controls and the webhook tester disappear, and what makes the run poller
   * clear the previous policy's decoration. Without the clear, takenEdges is
   * keyed by node name alone and a leftover trace would light up an inner node
   * that happens to share one.
   */
  selectedModule: string | null;
  // Deploy status of the currently open policy.
  policyStatus: PolicyStatus | null;
  /**
   * True when the stored policy has been saved since it was deployed.
   *
   * An edge's transFormat and a node's properties only reach the engine at
   * Policy.Init, which runs on Deploy. Saving while deployed therefore changes
   * the stored config and nothing else, and the run panel keeps showing the old
   * behavior with no hint why.
   */
  savedSinceDeploy: boolean;
  // Incrementing counter to force canvas reloads.
  canvasRevision: number;
  // Active toast notifications.
  toasts: Toast[];
  // Counter for generating unique toast ids.
  _nextToastId: number;
};

type AppActions = {
  selectService: (name: string | null) => void;
  selectPolicy: (name: string | null, status?: PolicyStatus) => void;
  selectModule: (name: string | null) => void;
  setPolicyStatus: (status: PolicyStatus) => void;
  markSavedSinceDeploy: (saved: boolean) => void;
  bumpCanvasRevision: () => void;
  pushToast: (message: string, kind: Toast['kind']) => void;
  dismissToast: (id: number) => void;
};

export const useStore = create<AppState & AppActions>((set) => ({
  selectedService: null,
  selectedPolicy: null,
  selectedModule: null,
  policyStatus: null,
  savedSinceDeploy: false,
  canvasRevision: 0,
  toasts: [],
  _nextToastId: 1,

  selectService: (name) =>
    set({
      selectedService: name,
      selectedPolicy: null,
      selectedModule: null,
      policyStatus: null,
      savedSinceDeploy: false,
    }),

  selectPolicy: (name, status) =>
    set({
      selectedPolicy: name,
      selectedModule: null,
      policyStatus: status ?? null,
      savedSinceDeploy: false,
    }),

  selectModule: (name) =>
    set({
      selectedModule: name,
      selectedPolicy: null,
      policyStatus: null,
      savedSinceDeploy: false,
    }),

  // Deploying or undeploying reloads the engine from the stored config, so the
  // two are back in step either way.
  setPolicyStatus: (status) => set({ policyStatus: status, savedSinceDeploy: false }),

  markSavedSinceDeploy: (saved) => set({ savedSinceDeploy: saved }),

  bumpCanvasRevision: () =>
    set((s) => ({ canvasRevision: s.canvasRevision + 1 })),

  pushToast: (message, kind) =>
    set((s) => ({
      toasts: [
        ...s.toasts,
        { id: s._nextToastId, message, kind },
      ],
      _nextToastId: s._nextToastId + 1,
    })),

  dismissToast: (id) =>
    set((s) => ({ toasts: s.toasts.filter((t) => t.id !== id) })),
}));
