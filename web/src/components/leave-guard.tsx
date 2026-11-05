import { useEffect } from "react";
import { useBlocker } from "react-router";
import { s } from "../strings";
import { ConfirmDialog } from "./dialog";

/**
 * Asks before leaving while `when` holds unsaved changes, both for
 * navigation inside the app and for closing or reloading the tab. Needs a
 * data router.
 */
export function LeaveGuard({ when }: { when: boolean }) {
  const blocker = useBlocker(when);
  useEffect(() => {
    if (!when) return;
    const h = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", h);
    return () => window.removeEventListener("beforeunload", h);
  }, [when]);
  if (blocker.state !== "blocked") return null;
  return <ConfirmDialog title={s.editor.unsaved} confirmLabel={s.editor.leave} body={s.editor.unsavedLeave} danger onClose={() => blocker.reset()} onConfirm={() => blocker.proceed()} />;
}
