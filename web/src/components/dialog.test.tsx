import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Dialog, useDialogGuard } from "./dialog";

afterEach(cleanup);

function backdrop(): Element {
  const node = screen.getByRole("dialog").parentElement;
  if (!node) throw new Error("no backdrop");
  return node;
}

function GuardedInput() {
  const [text, setText] = useState("");
  useDialogGuard(text !== "");
  return <input aria-label="note" value={text} onChange={(e) => setText(e.target.value)} />;
}

describe("Dialog", () => {
  it("closes on a backdrop click without unsaved input", () => {
    const onClose = vi.fn();
    render(<Dialog title="T" onClose={onClose}>body</Dialog>);
    fireEvent.mouseDown(screen.getByRole("dialog"));
    expect(onClose).not.toHaveBeenCalled();
    fireEvent.mouseDown(backdrop());
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("stays open on a backdrop click while dirty or pending, but Escape still closes", () => {
    const onClose = vi.fn();
    const view = render(<Dialog title="T" onClose={onClose} dirty>body</Dialog>);
    fireEvent.mouseDown(backdrop());
    view.rerender(<Dialog title="T" onClose={onClose} pending>body</Dialog>);
    fireEvent.mouseDown(backdrop());
    expect(onClose).not.toHaveBeenCalled();
    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("honours a guard declared by the content", () => {
    const onClose = vi.fn();
    render(
      <Dialog title="T" onClose={onClose}>
        <GuardedInput />
      </Dialog>,
    );
    fireEvent.change(screen.getByLabelText("note"), { target: { value: "draft" } });
    fireEvent.mouseDown(backdrop());
    expect(onClose).not.toHaveBeenCalled();
    fireEvent.change(screen.getByLabelText("note"), { target: { value: "" } });
    fireEvent.mouseDown(backdrop());
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});
