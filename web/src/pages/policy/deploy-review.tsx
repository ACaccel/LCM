import { useQuery } from "@tanstack/react-query";
import { policyVersionQuery } from "../../api/queries";
import { Dialog } from "../../components/dialog";
import { DiffView } from "../../components/diff-view";
import { ErrorBox, Spinner } from "../../components/ui";
import { docToYaml } from "../../lib/yamldoc";
import { s } from "../../strings";

/**
 * Confirmation before saving over a deployed policy: saving deploys, so the
 * diff is taken against the running version. `current` is the editor's
 * content in docToYaml form, so the diff shows content and not YAML style.
 */
export function DeployReview({
  name,
  deployedVersion,
  current,
  busy,
  error,
  onConfirm,
  onClose,
}: {
  name: string;
  deployedVersion: number;
  current: string;
  busy: boolean;
  error: unknown;
  onConfirm: () => void;
  onClose: () => void;
}) {
  const q = useQuery(policyVersionQuery(name, deployedVersion));
  return (
    <Dialog
      title={s.editor.reviewDeploy(deployedVersion)}
      wide
      onClose={onClose}
      pending={busy}
      footer={
        <>
          <button type="button" className="btn" onClick={onClose} data-autofocus>
            {s.common.cancel}
          </button>
          <button type="button" className="btn primary" disabled={busy || q.isPending} onClick={onConfirm}>
            {busy ? s.editor.saving : s.editor.save}
          </button>
        </>
      }
    >
      <div className="stack">
        <p className="meta">{s.editor.reviewDeployNote}</p>
        {q.isPending ? <Spinner /> : q.isError ? <ErrorBox error={q.error} onRetry={() => void q.refetch()} /> : <DiffView before={docToYaml(q.data.document)} after={current} />}
        <ErrorBox error={error} />
      </div>
    </Dialog>
  );
}
