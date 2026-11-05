import { useCallback, useState } from "react";
import { parsePolicyText } from "../../policy/model";
import type { ParseResult, PolicyModel } from "../../policy/model";

export interface EditorState {
  text: string;
  parsed: ParseResult;
  /** The last model that parsed, so the canvas stays put while the YAML is mid-edit. */
  model: PolicyModel | undefined;
}

/** An undoable edit: the text before it and the text it produced. */
interface Step {
  before: string;
  after: string;
}

interface State extends EditorState {
  undo: Step[];
}

const UNDO_LIMIT = 20;

function make(text: string, prev?: PolicyModel): EditorState {
  const parsed = parsePolicyText(text);
  return { text, parsed, model: parsed.ok ? parsed.model : prev };
}

/**
 * The editor's single source of truth is the YAML text. The canvas, the
 * property panel and the YAML view all read it and write it back through
 * these functions, which is what keeps the views in sync and lossless.
 *
 * Edits applied with `undoable` (deletions) can be taken back with `undo`
 * as long as nothing else changed the text since; any other edit makes the
 * recorded steps stale, so undo never discards later work.
 */
export function useEditorText(initial: string) {
  const [state, setState] = useState<State>(() => ({ ...make(initial), undo: [] }));

  const setText = useCallback((text: string) => setState((prev) => ({ ...make(text, prev.model), undo: prev.undo })), []);

  /** Applies a text-to-text edit to the latest text; edits that throw or change nothing are dropped. */
  const apply = useCallback(
    (edit: (text: string) => string, options?: { undoable?: boolean }) =>
      setState((prev) => {
        if (!prev.parsed.ok) return prev;
        try {
          const text = edit(prev.text);
          if (text === prev.text) return prev;
          const undo = options?.undoable ? [...prev.undo, { before: prev.text, after: text }].slice(-UNDO_LIMIT) : prev.undo;
          return { ...make(text, prev.model), undo };
        } catch {
          return prev;
        }
      }),
    [],
  );

  const undo = useCallback(
    () =>
      setState((prev) => {
        const last = prev.undo.at(-1);
        if (!last || last.after !== prev.text) return prev;
        return { ...make(last.before, prev.model), undo: prev.undo.slice(0, -1) };
      }),
    [],
  );

  const canUndo = state.undo.at(-1)?.after === state.text;

  return { state, setText, apply, undo, canUndo };
}
