import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import type { NodeType } from "../api/types";
import { PropsForm } from "./props-form";

const schema: NodeType = {
  type: "x.demo",
  category: "demo",
  effect: "none",
  ports: ["out"],
  props: [
    { name: "url", type: "string", required: true, description: "absolute URL" },
    { name: "method", type: "string", enum: ["GET", "POST"], default: "GET" },
    { name: "retries", type: "integer", default: 3 },
    { name: "verbose", type: "boolean" },
    { name: "condition", type: "boolean", expr: true },
    { name: "headers", type: "object", items: { name: "", type: "string" } },
    {
      name: "windows",
      type: "array",
      items: {
        name: "",
        type: "object",
        props: [
          { name: "start", type: "string", required: true },
          { name: "days", type: "array", items: { name: "", type: "string" } },
        ],
      },
    },
    { name: "payload", type: "any" },
  ],
};

function Harness({ initial = {}, onChange }: { initial?: Record<string, unknown>; onChange?: (v: Record<string, unknown>) => void }) {
  const [value, setValue] = useState(initial);
  return (
    <PropsForm
      schema={schema}
      value={value}
      onChange={(v) => {
        setValue(v);
        onChange?.(v);
      }}
    />
  );
}

describe("PropsForm", () => {
  it("renders labels, required markers, descriptions, defaults and enums from the schema", () => {
    render(<Harness />);
    expect(screen.getByLabelText(/^url/)).toBeInstanceOf(HTMLInputElement);
    expect(screen.getAllByLabelText("必填")).toHaveLength(1);
    expect(screen.getByText("absolute URL")).toBeTruthy();
    const method = screen.getByLabelText("method") as HTMLSelectElement;
    expect(within(method).getAllByRole("option").map((o) => o.textContent)).toEqual(["(GET)", "GET", "POST"]);
    expect((screen.getByLabelText("retries") as HTMLInputElement).placeholder).toBe("3");
  });

  it("emits edits and drops cleared fields", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();
    render(<Harness onChange={onChange} />);
    await user.type(screen.getByLabelText(/^url/), "http://x");
    expect(onChange).toHaveBeenLastCalledWith({ url: "http://x" });
    await user.selectOptions(screen.getByLabelText("method"), "POST");
    expect(onChange).toHaveBeenLastCalledWith({ url: "http://x", method: "POST" });
    await user.clear(screen.getByLabelText(/^url/));
    expect(onChange).toHaveBeenLastCalledWith({ method: "POST" });
  });

  it("types numbers, accepts templates and flags invalid numbers without emitting them", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();
    render(<Harness onChange={onChange} />);
    const retries = screen.getByLabelText("retries");
    await user.type(retries, "5");
    expect(onChange).toHaveBeenLastCalledWith({ retries: 5 });
    // user-event reads "{{" as an escaped brace, so set the template directly.
    fireEvent.change(retries, { target: { value: "{{ params.n }}" } });
    expect(onChange).toHaveBeenLastCalledWith({ retries: "{{ params.n }}" });
    onChange.mockClear();
    await user.clear(retries);
    await user.type(retries, "1.5");
    expect(screen.getByRole("alert").textContent).toContain("格式");
    expect(onChange).not.toHaveBeenCalledWith({ retries: 1.5 });
  });

  it("offers a tri-state select for booleans and a text input for expression booleans", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();
    render(<Harness onChange={onChange} />);
    await user.selectOptions(screen.getByLabelText("verbose"), "true");
    expect(onChange).toHaveBeenLastCalledWith({ verbose: true });
    const cond = screen.getByLabelText(/condition/);
    expect(cond).toBeInstanceOf(HTMLInputElement);
    fireEvent.change(cond, { target: { value: "trigger.ok == true" } });
    expect(onChange).toHaveBeenLastCalledWith({ verbose: true, condition: "trigger.ok == true" });
  });

  it("edits string maps and nested object lists", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();
    render(<Harness onChange={onChange} />);

    await user.type(screen.getByPlaceholderText("名稱"), "X-Env");
    await user.click(screen.getByRole("button", { name: /新增欄位/ }));
    expect(onChange).toHaveBeenLastCalledWith({ headers: { "X-Env": "" } });

    await user.click(screen.getByRole("button", { name: /新增項目/ }));
    expect(onChange).toHaveBeenLastCalledWith({ headers: { "X-Env": "" }, windows: [{}] });
    await user.type(screen.getByLabelText(/^start/), "09:00");
    expect(onChange).toHaveBeenLastCalledWith({ headers: { "X-Env": "" }, windows: [{ start: "09:00" }] });
    await user.click(screen.getByRole("button", { name: "移除 windows[0]" }));
    expect(onChange).toHaveBeenLastCalledWith({ headers: { "X-Env": "" }, windows: [] });
  });

  it("keeps a free-form value typed: JSON stays JSON, plain text stays text", () => {
    const onChange = vi.fn();
    render(<Harness onChange={onChange} />);
    const payload = screen.getByLabelText("payload");
    fireEvent.change(payload, { target: { value: '{"a":1}' } });
    expect(onChange).toHaveBeenLastCalledWith({ payload: { a: 1 } });
    fireEvent.change(payload, { target: { value: "hello" } });
    expect(onChange).toHaveBeenLastCalledWith({ payload: "hello" });
  });

  it("keeps props the schema does not know and lists them", () => {
    render(<Harness initial={{ url: "u", legacy: 1 }} />);
    expect(screen.getByText("legacy")).toBeTruthy();
  });

  it("shows issues located at a field", () => {
    render(
      <PropsForm
        schema={schema}
        value={{}}
        onChange={() => undefined}
        issues={[{ issue: { path: "spec.nodes[0].props.url", message: "is required" }, nodeId: "a", field: ["props", "url"] }]}
      />,
    );
    expect(screen.getByRole("alert").textContent).toBe("is required");
  });
});
