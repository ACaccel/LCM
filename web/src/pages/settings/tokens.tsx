import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import type { FormEvent } from "react";
import { client, unwrap } from "../../api/client";
import { qk, tokensQuery } from "../../api/queries";
import type { Token, TokenCreated, TokenScope } from "../../api/types";
import { ConfirmDialog, Dialog } from "../../components/dialog";
import { useToast } from "../../components/toast";
import { Async, Empty, ErrorBox, Field, Pill, Tag } from "../../components/ui";
import { formatDateTime } from "../../lib/format";
import { s } from "../../strings";

function CreateDialog({ onClose, onCreated }: { onClose: () => void; onCreated: (t: TokenCreated) => void }) {
  const [name, setName] = useState("");
  const [scope, setScope] = useState<TokenScope>("admin");
  const m = useMutation({
    mutationFn: () => unwrap(client.POST("/tokens", { body: { name: name.trim(), scope } })),
    onSuccess: onCreated,
  });
  function submit(e: FormEvent) {
    e.preventDefault();
    m.mutate();
  }
  return (
    <Dialog title={s.settings.tokens.create} onClose={onClose}>
      <form className="stack" onSubmit={submit}>
        <Field label={s.settings.tokens.name} htmlFor="tok-name">
          <input id="tok-name" type="text" value={name} maxLength={64} onChange={(e) => setName(e.target.value)} required data-autofocus />
        </Field>
        <Field label={s.settings.tokens.scope} htmlFor="tok-scope">
          <select id="tok-scope" value={scope} onChange={(e) => setScope(e.target.value as TokenScope)}>
            <option value="admin">{s.settings.tokens.scopeAdmin}</option>
            <option value="ingest">{s.settings.tokens.scopeIngest}</option>
          </select>
        </Field>
        <ErrorBox error={m.error} />
        <div className="row end">
          <button type="button" className="btn" onClick={onClose}>
            {s.common.cancel}
          </button>
          <button type="submit" className="btn primary" disabled={m.isPending || !name.trim()}>
            {s.common.create}
          </button>
        </div>
      </form>
    </Dialog>
  );
}

function ShownOnce({ token, onClose }: { token: TokenCreated; onClose: () => void }) {
  const toast = useToast();
  return (
    <Dialog title={s.settings.tokens.tokenValue} onClose={onClose} footer={<button type="button" className="btn primary" onClick={onClose}>{s.common.close}</button>}>
      <div className="stack">
        <div className="banner warn">{s.settings.tokens.shownOnce}</div>
        <pre className="block" data-testid="token-value">
          {token.token}
        </pre>
        <div>
          <button type="button" className="btn" onClick={() => void navigator.clipboard?.writeText(token.token).then(() => toast.success(s.common.copied))}>
            {s.common.copy}
          </button>
        </div>
      </div>
    </Dialog>
  );
}

export function TokensPanel() {
  const q = useQuery(tokensQuery());
  const qc = useQueryClient();
  const [creating, setCreating] = useState(false);
  const [shown, setShown] = useState<TokenCreated | null>(null);
  const [revoking, setRevoking] = useState<Token | null>(null);
  const revoke = useMutation({
    mutationFn: (id: number) => unwrap(client.DELETE("/tokens/{id}", { params: { path: { id } } })),
    onSuccess: () => {
      setRevoking(null);
      void qc.invalidateQueries({ queryKey: qk.tokens() });
    },
  });
  return (
    <div className="stack">
      <div className="row between">
        <p className="meta" style={{ margin: 0 }}>
          {s.settings.tokens.help}
        </p>
        <button type="button" className="btn primary" onClick={() => setCreating(true)}>
          {s.settings.tokens.create}
        </button>
      </div>
      <Async q={q}>
        {(items) =>
          items.length === 0 ? (
            <Empty>{s.settings.tokens.empty}</Empty>
          ) : (
            <div className="table-wrap">
              <table className="table">
                <thead>
                  <tr>
                    <th>{s.settings.tokens.name}</th>
                    <th>{s.settings.tokens.scope}</th>
                    <th>{s.settings.tokens.created}</th>
                    <th>{s.settings.tokens.lastUsed}</th>
                    <th>{s.common.status}</th>
                    <th>{s.common.actions}</th>
                  </tr>
                </thead>
                <tbody>
                  {items.map((t) => (
                    <tr key={t.id}>
                      <td>{t.name}</td>
                      <td>
                        <Tag>{t.scope}</Tag>
                      </td>
                      <td>{formatDateTime(t.createdAt)}</td>
                      <td>{t.lastUsedAt ? formatDateTime(t.lastUsedAt) : s.settings.tokens.neverUsed}</td>
                      <td>
                        {t.revokedAt ? (
                          <Pill view={{ tone: "neutral", icon: "–", label: s.settings.tokens.revoked }} title={formatDateTime(t.revokedAt)} />
                        ) : (
                          <Pill view={{ tone: "ok", icon: "✓", label: s.settings.tokens.active }} />
                        )}
                      </td>
                      <td>
                        {!t.revokedAt && (
                          <button type="button" className="btn small danger" onClick={() => setRevoking(t)}>
                            {s.settings.tokens.revoke}
                          </button>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )
        }
      </Async>
      {creating && (
        <CreateDialog
          onClose={() => setCreating(false)}
          onCreated={(t) => {
            setCreating(false);
            setShown(t);
            void qc.invalidateQueries({ queryKey: qk.tokens() });
          }}
        />
      )}
      {shown && <ShownOnce token={shown} onClose={() => setShown(null)} />}
      {revoking && (
        <ConfirmDialog
          title={s.settings.tokens.revoke}
          confirmLabel={s.settings.tokens.revoke}
          body={s.settings.tokens.confirmRevoke(revoking.name)}
          danger
          busy={revoke.isPending}
          error={revoke.error}
          onClose={() => setRevoking(null)}
          onConfirm={() => revoke.mutate(revoking.id)}
        />
      )}
    </div>
  );
}
