import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach, beforeEach, vi } from "vitest";

// jsdom has no matchMedia; tests run with a light system theme. Only the
// non-deprecated members: the app listens with addEventListener (theme.tsx).
type MediaQueryStub = Omit<MediaQueryList, "addListener" | "removeListener">;
vi.stubGlobal("matchMedia", (query: string): MediaQueryStub => ({
  matches: false,
  media: query,
  onchange: null,
  addEventListener: () => undefined,
  removeEventListener: () => undefined,
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

// jsdom has no IntersectionObserver (the library loads its next page when the
// end of the grid scrolls into view); nothing is laid out, so nothing ever
// comes into view.
vi.stubGlobal(
  "IntersectionObserver",
  class {
    observe(): void {
      // No layout in jsdom: nothing intersects.
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
// top, the command palette its active option into view); nothing is laid
// out, so there is nothing to scroll.
vi.stubGlobal("scrollTo", () => undefined);
Element.prototype.scrollIntoView = () => undefined;

beforeEach(() => {
  window.location.hash = "#/";
});

afterEach(() => {
  cleanup();
});

// The desktop app freezes Object.prototype before the page runs
// (tauri.conf.json security.freezePrototype); so do the tests.
Object.freeze(Object.prototype);
