import { useCallback, useState } from "react";
import { parsePolicyText } from "../../policy/model";
import type { ParseResult, PolicyModel } from "../../policy/model";

export interface EditorState {
  text: string;
  parsed: ParseResult;
  /** The last model that parsed, so the canvas stays put while the YAML is mid-edit. */
  model: PolicyModel | undefined;
}

function make(text: string, prev?: PolicyModel): EditorState {
  const parsed = parsePolicyText(text);
  return { text, parsed, model: parsed.ok ? parsed.model : prev };
}

/**
 * The editor's single source of truth is the YAML text. The canvas, the
 * property panel and the YAML view all read it and write it back through
 * these functions, which is what keeps the views in sync and lossless.
 */
export function useEditorText(initial: string) {
  const [state, setState] = useState(() => make(initial));

  const setText = useCallback((text: string) => setState((prev) => make(text, prev.model)), []);

  /** Applies a text-to-text edit to the latest text; edits that throw are dropped. */
  const apply = useCallback(
    (edit: (text: string) => string) =>
      setState((prev) => {
        if (!prev.parsed.ok) return prev;
        try {
          return make(edit(prev.text), prev.model);
        } catch {
          return prev;
        }
      }),
    [],
  );

  return { state, setText, apply };
}
