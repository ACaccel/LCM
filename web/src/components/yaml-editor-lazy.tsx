import { lazy, Suspense } from "react";
import type { YamlEditorProps } from "./yaml-editor";
import { Spinner } from "./ui";

// CodeMirror is the largest dependency and only some pages need it, so it
// loads on first use instead of with the application shell.
const Editor = lazy(() => import("./yaml-editor").then((m) => ({ default: m.YamlEditor })));

export function YamlEditor(props: YamlEditorProps) {
  return (
    <Suspense fallback={<Spinner />}>
      <Editor {...props} />
    </Suspense>
  );
}
