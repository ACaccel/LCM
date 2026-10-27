/**
 * RunPanel.tsx
 *
 * Execution results dock under the canvas.
 *
 * Shows the latest run of the selected policy: which nodes executed, in what
 * order, what each received and returned, and which branches stalled. Rows are
 * ordered by startOffsetMs rather than by seq, because a module's own event is
 * recorded after its internals finish and would otherwise sort below them.
 */

import { useMemo, useState } from 'react';
import { useStore } from '../hooks/useStore';
import { useRunStore } from '../hooks/useRunStore';
import type { RunNodeEvent } from '../model/types';
import '../styles/RunPanel.css';

type Props = {
  /** Re-runs the polling loop; wired to the panel's Refresh button. */
  onRefresh: () => void;
};

function formatJson(value: unknown): string {
  if (value === null || value === undefined) return 'null';
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

/** Groups module-internal events under the module node they belong to. */
function orderEvents(events: RunNodeEvent[]): RunNodeEvent[] {
  return [...events].sort((a, b) => {
    if (a.startOffsetMs !== b.startOffsetMs) return a.startOffsetMs - b.startOffsetMs;
    return a.seq - b.seq;
  });
}

function EventRow({ event }: { event: RunNodeEvent }): JSX.Element {
  const [open, setOpen] = useState(false);
  const hasError = event.inputErrors.some((e) => e !== '');
  const label = event.scope === '' ? event.node : `${event.scope} / ${event.node}`;

  return (
    <>
      <tr
        className={`run-row${open ? ' expanded' : ''}${event.scope !== '' ? ' nested' : ''}`}
        onClick={() => setOpen((v) => !v)}
      >
        <td className="run-col-seq">{event.seq}</td>
        <td className="run-col-node">
          <span className="run-caret">{open ? '▾' : '▸'}</span>
          {label}
          {hasError && <span className="run-error-dot" title="input transform failed" />}
        </td>
        <td className="run-col-type">{event.nodeType}</td>
        <td className="run-col-port">in {event.inPort}</td>
        <td className="run-col-port">out {event.ports.join(', ') || '-'}</td>
        <td className="run-col-time">+{event.startOffsetMs}ms</td>
        <td className="run-col-time">{event.durationMs}ms</td>
      </tr>
      {open && (
        <tr className="run-detail-row">
          <td colSpan={7}>
            <div className="run-detail">
              <div className="run-detail-block">
                <div className="run-detail-label">
                  input <span className="run-detail-hint">(after transFormat)</span>
                </div>
                <pre className="code-result">{formatJson(event.input)}</pre>
              </div>
              <div className="run-detail-block">
                <div className="run-detail-label">
                  inputRaw <span className="run-detail-hint">(upstream payload)</span>
                </div>
                <pre className="code-result">{formatJson(event.inputRaw)}</pre>
              </div>
              <div className="run-detail-block">
                <div className="run-detail-label">output</div>
                <pre className="code-result">{formatJson(event.output)}</pre>
              </div>
              {hasError && (
                <div className="run-detail-block">
                  <div className="run-detail-label error">inputErrors</div>
                  <pre className="code-result error">{formatJson(event.inputErrors)}</pre>
                </div>
              )}
            </div>
          </td>
        </tr>
      )}
    </>
  );
}

export default function RunPanel({ onRefresh }: Props): JSX.Element {
  const { selectedService, selectedPolicy, policyStatus } = useStore();
  const run = useRunStore((s) => s.run);
  const polling = useRunStore((s) => s.polling);
  const error = useRunStore((s) => s.error);
  const clearRun = useRunStore((s) => s.clearRun);
  const [collapsed, setCollapsed] = useState(false);

  const events = useMemo(() => orderEvents(run?.events ?? []), [run]);

  const hasPolicy = Boolean(selectedService && selectedPolicy);
  const isDeployed = policyStatus === 'DEPLOYED';

  let summary = 'no run yet';
  if (run?.hasRun) {
    summary = `${run.runId} · ${run.status} · ${run.eventCount} node${run.eventCount === 1 ? '' : 's'} · ${run.durationMs}ms`;
  } else if (polling) {
    summary = 'waiting for the first event…';
  }

  return (
    <section className={`run-panel${collapsed ? ' collapsed' : ''}`}>
      <header className="run-panel-header">
        <button
          className="run-collapse-btn"
          onClick={() => setCollapsed((v) => !v)}
          title={collapsed ? 'Expand' : 'Collapse'}
        >
          {collapsed ? '▸' : '▾'}
        </button>
        <span className="run-panel-title">Run</span>
        <span className={`status-badge run-status`} data-status={run?.status ?? 'no-run'}>
          {run?.status ?? 'no-run'}
        </span>
        <span className="run-panel-summary">{summary}</span>
        {polling && <span className="run-spinner" title="polling" />}
        <div className="run-panel-actions">
          <button className="btn-secondary" onClick={onRefresh} disabled={!hasPolicy || !isDeployed}>
            Refresh
          </button>
          <button className="btn-secondary" onClick={clearRun} disabled={!run}>
            Clear
          </button>
        </div>
      </header>

      {!collapsed && (
        <div className="run-panel-body">
          {!hasPolicy && <div className="run-empty">Select a service and policy to see its execution results.</div>}
          {hasPolicy && !isDeployed && (
            <div className="run-empty">Policy is not deployed. Deploy it, then fire a webhook to record a run.</div>
          )}
          {hasPolicy && isDeployed && error && <div className="run-empty error">{error}</div>}
          {hasPolicy && isDeployed && !error && !run?.hasRun && (
            <div className="run-empty">No run recorded yet. Fire a webhook from the Inspector.</div>
          )}

          {run?.hasRun && (
            <>
              <div className="run-trigger">
                triggered by <strong>{run.trigger.node}</strong> ({run.trigger.nodeType}) with{' '}
                <code>{formatJson(run.trigger.content)}</code>
              </div>

              {run.waiting.length > 0 && (
                <div className="run-waiting">
                  stalled: {run.waiting.map((w) => `${w.scope ? w.scope + '/' : ''}${w.node} port ${w.port}`).join(', ')}
                  <span className="run-detail-hint"> (input never produced in this run)</span>
                </div>
              )}
              {run.truncated && (
                <div className="run-waiting">
                  trace truncated at {events.length} of {run.eventCount} events
                </div>
              )}

              <table className="run-table">
                <thead>
                  <tr>
                    <th className="run-col-seq">#</th>
                    <th className="run-col-node">node</th>
                    <th className="run-col-type">type</th>
                    <th className="run-col-port">in</th>
                    <th className="run-col-port">out</th>
                    <th className="run-col-time">at</th>
                    <th className="run-col-time">took</th>
                  </tr>
                </thead>
                <tbody>
                  {events.map((ev) => (
                    <EventRow key={`${ev.scope}-${ev.seq}`} event={ev} />
                  ))}
                </tbody>
              </table>
            </>
          )}
        </div>
      )}
    </section>
  );
}
