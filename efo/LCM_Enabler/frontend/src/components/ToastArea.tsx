/**
 * ToastArea.tsx
 *
 * Renders the stack of active toast notifications and auto-dismisses them
 * after a timeout. Toasts stack at the bottom-right of the viewport.
 */

import { useEffect } from 'react';
import { useStore } from '../hooks/useStore';
import '../styles/ToastArea.css';

const AUTO_DISMISS_MS = 4000;

export default function ToastArea() {
  const { toasts, dismissToast } = useStore();

  // Auto-dismiss each toast after its lifetime.
  useEffect(() => {
    if (toasts.length === 0) return;
    const latest = toasts[toasts.length - 1];
    const timer = setTimeout(() => dismissToast(latest.id), AUTO_DISMISS_MS);
    return () => clearTimeout(timer);
  }, [toasts, dismissToast]);

  return (
    <div className="toast-area" aria-live="polite">
      {toasts.map((t) => (
        <div key={t.id} className={`toast toast-${t.kind}`}>
          <span className="toast-msg">{t.message}</span>
          <button className="toast-close" onClick={() => dismissToast(t.id)} aria-label="Dismiss">
            ×
          </button>
        </div>
      ))}
    </div>
  );
}
