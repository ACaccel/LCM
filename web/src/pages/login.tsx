import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import type { FormEvent } from "react";
import { useNavigate, useSearchParams } from "react-router";
import { client, unwrap } from "../api/client";
import { qk } from "../api/queries";
import { ErrorBox } from "../components/ui";
import { s } from "../strings";

/** Only same-origin absolute paths are followed after login. */
export function safeNext(raw: string | null): string {
  if (!raw || !raw.startsWith("/") || raw.startsWith("//") || raw.startsWith("/\\")) return "/";
  return raw;
}

export function LoginPage() {
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [username, setUsername] = useState("admin");
  const [password, setPassword] = useState("");

  const login = useMutation({
    mutationFn: () => unwrap(client.POST("/auth/login", { body: { username, password } })),
    onSuccess: (res) => {
      qc.setQueryData(qk.me(), { actor: res.actor });
      void navigate(safeNext(params.get("next")), { replace: true });
    },
  });

  function submit(e: FormEvent) {
    e.preventDefault();
    login.mutate();
  }

  return (
    <div className="auth-page">
      <form className="panel auth-card stack" onSubmit={submit}>
        <h1>{s.login.title}</h1>
        <div className="field">
          <label htmlFor="username">{s.login.username}</label>
          <input id="username" type="text" autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} required />
        </div>
        <div className="field">
          <label htmlFor="password">{s.login.password}</label>
          <input
            id="password"
            type="password"
            autoComplete="current-password"
            autoFocus
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            required
          />
        </div>
        <ErrorBox error={login.error} />
        <button type="submit" className="btn primary" disabled={login.isPending}>
          {login.isPending ? s.login.submitting : s.login.submit}
        </button>
        <p className="meta">{s.login.hint}</p>
      </form>
    </div>
  );
}
