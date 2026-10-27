/**
 * useRunPolling.ts
 *
 * Polls the run endpoint after a trigger fires.
 *
 * The engine has no completion callback, so the backend publishes a status and
 * the client polls until it settles. Polling stops as soon as the run reports
 * complete (after one final fetch to pick up the tail), on error, or at a hard
 * time limit, so a stalled policy cannot leave the browser polling forever.
 */

import { useCallback, useEffect, useRef } from 'react';
import { getPolicyRun } from '../api/lcmApi';
import { useRunStore } from './useRunStore';

const POLL_INTERVAL_MS = 800;
const MAX_POLL_DURATION_MS = 30_000;

type StartRunPolling = () => void;

/**
 * Returns a callback that starts (or restarts) polling for the given policy.
 * Polling stops automatically when the selection changes or the component
 * unmounts.
 */
export function useRunPolling(
  svcName: string | null,
  policyName: string | null,
): StartRunPolling {
  const timerRef = useRef<number | null>(null);
  const sinceRef = useRef(0);
  const runIdRef = useRef<string>('');
  const deadlineRef = useRef(0);
  // Guards the async callback against acting after the effect cleanup ran.
  const activeRef = useRef(false);

  const stop = useCallback(() => {
    activeRef.current = false;
    if (timerRef.current !== null) {
      window.clearTimeout(timerRef.current);
      timerRef.current = null;
    }
    useRunStore.getState().setPolling(false);
  }, []);

  const poll = useCallback(
    async (svc: string, policy: string) => {
      if (!activeRef.current) return;

      const store = useRunStore.getState();
      try {
        const trace = await getPolicyRun(svc, policy, sinceRef.current);
        if (!activeRef.current) return;

        // A new runId supersedes what we accumulated; refetch from the start.
        if (trace.runId !== runIdRef.current) {
          runIdRef.current = trace.runId;
          sinceRef.current = 0;
          if (trace.since !== 0) {
            timerRef.current = window.setTimeout(() => void poll(svc, policy), 0);
            return;
          }
        }

        store.mergeRun(trace);
        if (trace.events.length > 0) {
          sinceRef.current = trace.events[trace.events.length - 1].seq;
        }

        const settled = trace.status === 'complete' || trace.status === 'no-run';
        const expired = Date.now() > deadlineRef.current;
        if (settled || expired) {
          stop();
          return;
        }
        timerRef.current = window.setTimeout(() => void poll(svc, policy), POLL_INTERVAL_MS);
      } catch (err: unknown) {
        if (!activeRef.current) return;
        // Reported in the panel rather than as a toast: a policy that is not
        // deployed would otherwise produce one toast per tick.
        store.setError(String(err));
        stop();
      }
    },
    [stop],
  );

  const start = useCallback(() => {
    if (!svcName || !policyName) return;
    stop();
    sinceRef.current = 0;
    runIdRef.current = '';
    deadlineRef.current = Date.now() + MAX_POLL_DURATION_MS;
    activeRef.current = true;
    useRunStore.getState().setPolling(true);
    void poll(svcName, policyName);
  }, [svcName, policyName, stop, poll]);

  // A different policy has a different trace; drop what is on screen.
  useEffect(() => {
    stop();
    useRunStore.getState().clearRun();
    return stop;
  }, [svcName, policyName, stop]);

  return start;
}
