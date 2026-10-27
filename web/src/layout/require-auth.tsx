import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Navigate, Outlet, useLocation } from "react-router";
import { isApiError } from "../api/client";
import { meQuery } from "../api/queries";
import { ErrorBox, Spinner } from "../components/ui";
import { Shell } from "./shell";

/** Gate for the console: redirects to the login page when there is no session. */
export function RequireAuth() {
  const me = useQuery(meQuery());
  const loc = useLocation();
  const qc = useQueryClient();

  if (me.isError && isApiError(me.error) && me.error.status === 401) {
    const next = loc.pathname + loc.search;
    return <Navigate to={next === "/" ? "/login" : `/login?next=${encodeURIComponent(next)}`} replace />;
  }
  if (me.isError) {
    return (
      <div className="auth-page">
        <ErrorBox error={me.error} onRetry={() => void qc.refetchQueries({ queryKey: meQuery().queryKey })} />
      </div>
    );
  }
  if (me.isPending) {
    return (
      <div className="auth-page">
        <Spinner />
      </div>
    );
  }
  return (
    <Shell actor={me.data.actor}>
      <Outlet />
    </Shell>
  );
}
