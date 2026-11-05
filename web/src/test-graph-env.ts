import { vi } from "vitest";

/**
 * jsdom has no layout engine. React Flow only mounts nodes once its container
 * has a size, and needs ResizeObserver and DOMMatrixReadOnly; call this in a
 * test file that renders a real graph.
 */
export function stubGraphEnvironment(): void {
  Object.defineProperty(HTMLElement.prototype, "offsetWidth", { configurable: true, value: 800 });
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", { configurable: true, value: 500 });
  class RO {
    constructor(private cb?: ResizeObserverCallback) {}
    observe(el?: Element) {
      this.cb?.([{ target: el, contentRect: { width: 800, height: 500 } } as unknown as ResizeObserverEntry], this as unknown as ResizeObserver);
    }
    unobserve() {}
    disconnect() {}
  }
  vi.stubGlobal("ResizeObserver", RO);
  vi.stubGlobal(
    "DOMMatrixReadOnly",
    class {
      m22 = 1;
      constructor(public transform?: string) {}
    },
  );
}
