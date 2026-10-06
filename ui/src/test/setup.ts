import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach, beforeEach } from "vitest";

// jsdom has no matchMedia; tests run with a light system theme.
window.matchMedia = (query: string): MediaQueryList => ({
  matches: false,
  media: query,
  onchange: null,
  addEventListener: () => undefined,
  removeEventListener: () => undefined,
  addListener: () => undefined,
  removeListener: () => undefined,
  dispatchEvent: () => false,
});

// jsdom has no ResizeObserver (Fluent's MessageBar watches its own size); nothing
// is laid out in tests, so observed elements never resize.
window.ResizeObserver = class {
  observe(): void {
    // No layout in jsdom: there is nothing to observe.
  }
  unobserve(): void {
    // Nothing was observed.
  }
  disconnect(): void {
    // Nothing was observed.
  }
};

beforeEach(() => {
  window.location.hash = "#/";
});

afterEach(() => {
  cleanup();
});
