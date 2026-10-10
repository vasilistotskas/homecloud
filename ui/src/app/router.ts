import { useSyncExternalStore } from "react";

/**
 * The app's routes. Routing is hash-based (#/library, #/connect?code=…), so it
 * works the same when the agent serves the app and inside the desktop shell,
 * with no server rewrites. It listens to `hashchange` (Baseline widely
 * available since 2015) rather than the Navigation API: that is Baseline only
 * since 2026, and WebKitGTK, the Linux desktop app's engine, enables it by
 * default only from 2.52 (2.48 and 2.50 ship it off).
 */
export const paths = [
  "/",
  "/library",
  "/activity",
  "/quarantine",
  "/safe-to-delete",
  "/settings",
  "/connect",
] as const satisfies readonly `/${string}`[];
export type Path = (typeof paths)[number];

/** Where the app is: a known path (undefined when the hash names no page) and its search parameters. */
export interface Location {
  path: Path | undefined;
  search: URLSearchParams;
}

export function isPath(p: string): p is Path {
  return (paths as readonly string[]).includes(p);
}

/**
 * Parses a location hash. No hash, "#" and "#/" are the overview; anything
 * else must name a page exactly.
 */
export function parseHash(hash: string): Location {
  const raw = hash.startsWith("#") ? hash.slice(1) : hash;
  const q = raw.indexOf("?");
  const pathname = (q < 0 ? raw : raw.slice(0, q)) || "/";
  const search = new URLSearchParams(q < 0 ? "" : raw.slice(q + 1));
  return { path: isPath(pathname) ? pathname : undefined, search };
}

/** The hash for a path and its search parameters. */
export function href(path: Path, search?: Record<string, string>): string {
  const query = search === undefined ? "" : new URLSearchParams(search).toString();
  return `#${path}${query === "" ? "" : `?${query}`}`;
}

// Subscribers to location changes: the browser's hashchange covers links, the
// back and forward buttons and pushes; a replace does not fire it, so
// navigate notifies them itself.
const listeners = new Set<() => void>();

function notify(): void {
  for (const l of listeners) {
    l();
  }
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  window.addEventListener("hashchange", listener);
  return () => {
    listeners.delete(listener);
    window.removeEventListener("hashchange", listener);
  };
}

/**
 * Goes to path. A push adds a history entry (back returns here); a replace
 * swaps the current one, for locations that should not be returned to.
 */
export function navigate(path: Path, opts: { replace?: boolean; search?: Record<string, string> } = {}): void {
  const target = href(path, opts.search);
  if (window.location.hash === target) {
    return;
  }
  if (opts.replace === true) {
    window.history.replaceState(window.history.state, "", target);
    notify();
    return;
  }
  // Setting the hash adds a history entry and fires hashchange.
  window.location.hash = target;
}

/** The current location; components re-render when it changes. */
export function useLocation(): Location {
  // URLSearchParams is not comparable, so the snapshot is the hash string,
  // parsed on each render (a few string operations).
  return parseHash(useSyncExternalStore(subscribe, () => window.location.hash));
}
