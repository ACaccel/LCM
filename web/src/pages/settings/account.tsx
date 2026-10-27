import { useQuery } from "@tanstack/react-query";
import { meQuery } from "../../api/queries";
import { Async } from "../../components/ui";
import { s } from "../../strings";

export function AccountPanel() {
  const me = useQuery(meQuery());
  return (
    <div className="stack" style={{ maxWidth: 560 }}>
      <h2>{s.settings.accountPanel.title}</h2>
      <Async q={me}>
        {(d) => (
          <dl className="kv">
            <dt>{s.settings.accountPanel.user}</dt>
            <dd>{d.actor.name}</dd>
            <dt>{s.settings.accountPanel.kind}</dt>
            <dd>{d.actor.kind}</dd>
          </dl>
        )}
      </Async>
      <h3>{s.settings.accountPanel.passwordTitle}</h3>
      <p>{s.settings.accountPanel.passwordHelp}</p>
      <pre className="block">{s.settings.accountPanel.passwordCommand}</pre>
    </div>
  );
}
