import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach, beforeEach, vi } from "vitest";

// jsdom has no matchMedia; tests run with a light system theme.
vi.stubGlobal("matchMedia", (query: string): MediaQueryList => ({
  matches: false,
  media: query,
  onchange: null,
  addEventListener: () => undefined,
  removeEventListener: () => undefined,
  addListener: () => undefined,
  removeListener: () => undefined,
  dispatchEvent: () => false,
}));

// jsdom has no ResizeObserver (Fluent's MessageBar watches its own size); nothing
// is laid out in tests, so observed elements never resize.
vi.stubGlobal(
  "ResizeObserver",
  class {
    observe(): void {
      // No layout in jsdom: there is nothing to observe.
    }
    unobserve(): void {
      // Nothing was observed.
    }
    disconnect(): void {
      // Nothing was observed.
    }
  },
);

// jsdom does not implement scrolling (the shell scrolls each new page to its
// top); nothing is laid out, so there is nothing to scroll.
vi.stubGlobal("scrollTo", () => undefined);

beforeEach(() => {
  window.location.hash = "#/";
});

afterEach(() => {
  cleanup();
});

// The desktop app freezes Object.prototype before the page runs
// (tauri.conf.json security.freezePrototype); so do the tests.
Object.freeze(Object.prototype);
