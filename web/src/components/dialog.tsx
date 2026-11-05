import { createContext, useContext, useEffect, useId, useMemo, useRef } from "react";
import type { ReactNode } from "react";
import { s } from "../strings";
import { ErrorBox } from "./ui";

const FOCUSABLE = 'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

interface GuardRegistry {
  add: (id: symbol) => void;
  remove: (id: symbol) => void;
}

const GuardContext = createContext<GuardRegistry | null>(null);

/**
 * Declares, from inside a dialog's content, that it holds unsaved input or
 * a request in flight; while `active`, a backdrop click does not close the
 * enclosing dialog. For content that keeps its state below the Dialog.
 */
export function useDialogGuard(active: boolean) {
  const registry = useContext(GuardContext);
  useEffect(() => {
    if (!active || !registry) return;
    const id = Symbol("dialog-guard");
    registry.add(id);
    return () => registry.remove(id);
  }, [active, registry]);
}

/**
 * A modal dialog with a focus trap; Escape and the backdrop close it.
 * A stray backdrop click must not throw work away: while `dirty` (unsaved
 * input), `pending` (a request in flight) or a `useDialogGuard` in the
 * content is active, only Escape and the dialog's own buttons close it.
 */
export function Dialog({
  title,
  onClose,
  children,
  footer,
  wide,
  dirty,
  pending,
}: {
  title: string;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
  wide?: boolean;
  dirty?: boolean;
  pending?: boolean;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const titleId = useId();
  const onCloseRef = useRef(onClose);
  useEffect(() => {
    onCloseRef.current = onClose;
  });
  const guards = useRef(new Set<symbol>());
  const registry = useMemo<GuardRegistry>(
    () => ({
      add: (id) => guards.current.add(id),
      remove: (id) => guards.current.delete(id),
    }),
    [],
  );

  function onBackdrop(e: React.MouseEvent) {
    if (e.target !== e.currentTarget || dirty || pending || guards.current.size > 0) return;
    onClose();
  }

  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null;
    const node = ref.current;
    const first = node?.querySelector<HTMLElement>("[data-autofocus]") ?? node?.querySelector<HTMLElement>(FOCUSABLE);
    (first ?? node)?.focus();
    return () => previous?.focus?.();
  }, []);

  function onKeyDown(e: React.KeyboardEvent) {
    if (e.key === "Escape") {
      e.stopPropagation();
      onCloseRef.current();
      return;
    }
    if (e.key !== "Tab" || !ref.current) return;
    const items = Array.from(ref.current.querySelectorAll<HTMLElement>(FOCUSABLE));
    if (items.length === 0) return;
    const first = items[0];
    const last = items[items.length - 1];
    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault();
      last?.focus();
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault();
      first?.focus();
    }
  }

  return (
    <div className="dialog-backdrop" onMouseDown={onBackdrop}>
      <div
        ref={ref}
        className={`dialog${wide ? " wide" : ""}`}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        onKeyDown={onKeyDown}
      >
        <h2 id={titleId}>{title}</h2>
        <GuardContext.Provider value={registry}>
          {children}
          {footer && <div className="footer">{footer}</div>}
        </GuardContext.Provider>
      </div>
    </div>
  );
}

/** Confirmation for a destructive or irreversible step. */
export function ConfirmDialog({
  title,
  body,
  confirmLabel = s.common.confirm,
  danger,
  busy,
  disabled,
  error,
  onConfirm,
  onClose,
}: {
  title: string;
  body: ReactNode;
  confirmLabel?: string;
  danger?: boolean;
  busy?: boolean;
  /** Keeps the confirm button disabled, for example until an acknowledgement is given. */
  disabled?: boolean;
  error?: unknown;
  onConfirm: () => void;
  onClose: () => void;
}) {
  return (
    <Dialog
      title={title}
      onClose={onClose}
      pending={busy}
      footer={
        <>
          <button type="button" className="btn" onClick={onClose} data-autofocus>
            {s.common.cancel}
          </button>
          <button type="button" className={`btn ${danger ? "danger solid" : "primary"}`} disabled={busy || disabled} onClick={onConfirm}>
            {confirmLabel}
          </button>
        </>
      }
    >
      <div className="stack">
        <div>{body}</div>
        <ErrorBox error={error} />
      </div>
    </Dialog>
  );
}
