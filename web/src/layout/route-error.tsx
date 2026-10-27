import { isRouteErrorResponse, useRouteError } from "react-router";
import { ErrorBox } from "../components/ui";
import { s } from "../strings";

/** Last-resort boundary: render errors and failed lazy imports show up here. */
export function RouteError() {
  const error = useRouteError();
  const shown = isRouteErrorResponse(error) ? new Error(`${error.status} ${error.statusText}`) : error;
  return (
    <div className="page">
      <ErrorBox error={shown} />
      <p>
        <button type="button" className="btn" onClick={() => window.location.reload()}>
          {s.common.reload}
        </button>
      </p>
    </div>
  );
}
