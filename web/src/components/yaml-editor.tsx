import { defaultKeymap, history, historyKeymap } from "@codemirror/commands";
import { yaml } from "@codemirror/lang-yaml";
import { HighlightStyle, bracketMatching, indentOnInput, syntaxHighlighting } from "@codemirror/language";
import { Annotation, EditorState } from "@codemirror/state";
import { EditorView, highlightActiveLine, keymap, lineNumbers } from "@codemirror/view";
import { tags as t } from "@lezer/highlight";
import { useEffect, useRef } from "react";

// Colours come from the page's CSS variables so the editor follows the
// light and dark theme without a second palette.
const theme = EditorView.theme({
  "&": { backgroundColor: "var(--card)", color: "var(--foreground)", border: "1px solid var(--border)", borderRadius: "8px", fontSize: "12.5px" },
  "&.cm-focused": { outline: "2px solid var(--ring)", outlineOffset: "1px" },
  ".cm-scroller": { fontFamily: "var(--font-mono)", lineHeight: "1.5", minHeight: "var(--editor-min, 220px)", maxHeight: "var(--editor-max, 70vh)" },
  ".cm-gutters": { backgroundColor: "var(--muted)", color: "var(--muted-foreground)", border: "0", borderRadius: "8px 0 0 8px" },
  ".cm-activeLine": { backgroundColor: "color-mix(in srgb, var(--muted) 60%, transparent)" },
  ".cm-activeLineGutter": { backgroundColor: "var(--border)" },
  ".cm-cursor": { borderLeftColor: "var(--foreground)" },
  "&.cm-focused .cm-selectionBackground, .cm-selectionBackground": { backgroundColor: "color-mix(in srgb, var(--primary) 28%, transparent)" },
});

const highlight = HighlightStyle.define([
  { tag: [t.propertyName, t.definition(t.propertyName)], color: "var(--chart-1)" },
  { tag: [t.string, t.special(t.string)], color: "var(--chart-2)" },
  { tag: [t.number, t.bool, t.null, t.atom], color: "var(--chart-4)" },
  { tag: t.comment, color: "var(--muted-foreground)", fontStyle: "italic" },
  { tag: [t.meta, t.labelName, t.typeName], color: "var(--chart-3)" },
  { tag: [t.punctuation, t.separator], color: "var(--muted-foreground)" },
]);

/** Marks changes that came from the parent, so they are not reported back as edits. */
const external = Annotation.define<boolean>();

export interface YamlEditorProps {
  value: string;
  onChange?: (value: string) => void;
  readOnly?: boolean;
  ariaLabel: string;
  /** Minimum height in px. */
  minHeight?: number;
}

/**
 * CodeMirror 6 editor for YAML. The text is owned by the parent: external
 * changes replace the document, edits are reported through onChange.
 * Tab keeps its browser meaning so the editor never traps keyboard users.
 */
export function YamlEditor({ value, onChange, readOnly, ariaLabel, minHeight = 220 }: YamlEditorProps) {
  const host = useRef<HTMLDivElement>(null);
  const view = useRef<EditorView | null>(null);
  const onChangeRef = useRef(onChange);
  const valueRef = useRef(value);
  useEffect(() => {
    onChangeRef.current = onChange;
    valueRef.current = value;
  });

  useEffect(() => {
    if (!host.current) return;
    const v = new EditorView({
      parent: host.current,
      state: EditorState.create({
        doc: valueRef.current,
        extensions: [
          lineNumbers(),
          history(),
          indentOnInput(),
          bracketMatching(),
          highlightActiveLine(),
          yaml(),
          syntaxHighlighting(highlight),
          theme,
          keymap.of([...defaultKeymap, ...historyKeymap]),
          EditorState.readOnly.of(!!readOnly),
          EditorView.editable.of(!readOnly),
          EditorView.contentAttributes.of({ "aria-label": ariaLabel, "aria-multiline": "true" }),
          EditorView.updateListener.of((u) => {
            if (u.docChanged && !u.transactions.some((tr) => tr.annotation(external))) onChangeRef.current?.(u.state.doc.toString());
          }),
        ],
      }),
    });
    view.current = v;
    return () => {
      v.destroy();
      view.current = null;
    };
    // The editor is rebuilt only when its read-only mode or label changes;
    // text updates are applied below without losing the undo history.
  }, [readOnly, ariaLabel]);

  useEffect(() => {
    const v = view.current;
    if (v && v.state.doc.toString() !== value) {
      v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: value }, annotations: external.of(true) });
    }
  }, [value]);

  return <div ref={host} style={{ ["--editor-min" as string]: `${minHeight}px` }} />;
}
