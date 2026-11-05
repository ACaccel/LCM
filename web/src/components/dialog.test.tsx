import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { s as strings } from "../strings";
import { ConfirmDialog, Dialog, useDialogGuard } from "./dialog";

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

describe("Dialog accessibility", () => {
  it("is a modal dialog named by its title, with the footer after the content", () => {
    render(
      <Dialog title="Edit note" onClose={() => undefined} footer={<button type="button">Save</button>}>
        <p>body</p>
      </Dialog>,
    );
    const dialog = screen.getByRole("dialog", { name: "Edit note" });
    expect(dialog.getAttribute("aria-modal")).toBe("true");
    const text = dialog.textContent ?? "";
    expect(text.indexOf("body")).toBeLessThan(text.indexOf("Save"));
  });

  it("focuses the marked field first, else the first focusable element", () => {
    const view = render(
      <Dialog title="T" onClose={() => undefined}>
        <button type="button">first</button>
        <input aria-label="preferred" data-autofocus />
      </Dialog>,
    );
    expect(document.activeElement).toBe(screen.getByLabelText("preferred"));
    view.unmount();
    render(
      <Dialog title="T" onClose={() => undefined}>
        <button type="button">first</button>
        <button type="button">second</button>
      </Dialog>,
    );
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "first" }));
  });

  it("gives focus back to the opener when it closes", () => {
    const opener = document.createElement("button");
    document.body.append(opener);
    opener.focus();
    const view = render(<Dialog title="T" onClose={() => undefined}>body</Dialog>);
    expect(document.activeElement).not.toBe(opener);
    view.unmount();
    expect(document.activeElement).toBe(opener);
    opener.remove();
  });

  it("keeps Tab and Shift+Tab inside the dialog", async () => {
    const user = userEvent.setup();
    render(
      <Dialog title="T" onClose={() => undefined}>
        <button type="button">one</button>
        <button type="button">two</button>
      </Dialog>,
    );
    const one = screen.getByRole("button", { name: "one" });
    const two = screen.getByRole("button", { name: "two" });
    expect(document.activeElement).toBe(one);
    await user.tab({ shift: true });
    expect(document.activeElement).toBe(two);
    await user.tab();
    expect(document.activeElement).toBe(one);
  });

  it("does not let Escape reach handlers outside the dialog", () => {
    const outer = vi.fn();
    const onClose = vi.fn();
    render(
      <div onKeyDown={outer}>
        <Dialog title="T" onClose={onClose}>
          body
        </Dialog>
      </div>,
    );
    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(outer).not.toHaveBeenCalled();
  });

  it("calls the latest onClose, not the one it was opened with", () => {
    const first = vi.fn();
    const second = vi.fn();
    const view = render(<Dialog title="T" onClose={first}>body</Dialog>);
    view.rerender(<Dialog title="T" onClose={second}>body</Dialog>);
    fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
    expect(first).not.toHaveBeenCalled();
    expect(second).toHaveBeenCalledTimes(1);
  });
});

describe("ConfirmDialog", () => {
  it("confirms and cancels through its two buttons", async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    const onClose = vi.fn();
    render(<ConfirmDialog title="Remove" body="Remove the thing?" confirmLabel="Remove it" onConfirm={onConfirm} onClose={onClose} />);
    expect(screen.getByText("Remove the thing?")).toBeTruthy();
    // Cancel is focused first so a stray Enter cannot confirm.
    expect(document.activeElement).toBe(screen.getByRole("button", { name: strings.common.cancel }));
    await user.click(screen.getByRole("button", { name: "Remove it" }));
    expect(onConfirm).toHaveBeenCalledTimes(1);
    await user.click(screen.getByRole("button", { name: strings.common.cancel }));
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("disables confirming while busy or not yet allowed, and keeps the backdrop inert while busy", () => {
    const onClose = vi.fn();
    const view = render(<ConfirmDialog title="T" body="b" busy onConfirm={() => undefined} onClose={onClose} />);
    expect((screen.getByRole("button", { name: strings.common.confirm }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.mouseDown(backdrop());
    expect(onClose).not.toHaveBeenCalled();
    view.rerender(<ConfirmDialog title="T" body="b" disabled onConfirm={() => undefined} onClose={onClose} />);
    expect((screen.getByRole("button", { name: strings.common.confirm }) as HTMLButtonElement).disabled).toBe(true);
    view.rerender(<ConfirmDialog title="T" body="b" onConfirm={() => undefined} onClose={onClose} />);
    expect((screen.getByRole("button", { name: strings.common.confirm }) as HTMLButtonElement).disabled).toBe(false);
  });

  it("shows the failure of the confirmed step and styles dangerous confirmations", () => {
    render(<ConfirmDialog title="T" body="b" danger error={new Error("Server said no")} onConfirm={() => undefined} onClose={() => undefined} />);
    expect(screen.getByRole("alert").textContent).toContain("Server said no");
    expect(screen.getByRole("button", { name: strings.common.confirm }).className).toContain("danger");
  });
});
