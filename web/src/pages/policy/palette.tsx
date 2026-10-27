import { useState } from "react";
import type { NodeType } from "../../api/types";
import { NODE_TYPE_MIME } from "../../components/graph";
import { categoryColor, groupNodeTypes } from "../../policy/schema";
import { s } from "../../strings";

/** Node types grouped by category, all taken from the server's schemas. */
export function Palette({ types, onAdd }: { types: readonly NodeType[]; onAdd: (type: string) => void }) {
  const [search, setSearch] = useState("");
  const groups = groupNodeTypes(types, search);
  return (
    <div className="panel palette" aria-label={s.editor.palette}>
      <div className="field">
        <label htmlFor="pal-search" className="sr-only">
          {s.editor.searchNodes}
        </label>
        <input id="pal-search" type="search" placeholder={s.editor.searchNodes} value={search} onChange={(e) => setSearch(e.target.value)} />
      </div>
      {groups.map((g) => (
        <details key={g.category} open={!!search || g.types.length < 8}>
          <summary>{g.category}</summary>
          {g.types.map((t) => (
            <button
              key={t.type}
              type="button"
              className="item"
              style={{ ["--cat" as string]: categoryColor(t.category) }}
              title={t.description}
              draggable
              onDragStart={(e) => {
                e.dataTransfer.setData(NODE_TYPE_MIME, t.type);
                e.dataTransfer.effectAllowed = "copy";
              }}
              onClick={() => onAdd(t.type)}
            >
              {t.type}
            </button>
          ))}
        </details>
      ))}
    </div>
  );
}
