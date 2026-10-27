import { Link } from "react-router";
import { s } from "../strings";

export function NotFoundPage() {
  return (
    <div className="page">
      <h1>{s.notFound.title}</h1>
      <p>{s.notFound.body}</p>
      <Link to="/">{s.nav.overview}</Link>
    </div>
  );
}
