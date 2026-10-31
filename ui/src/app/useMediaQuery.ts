import { useSyncExternalStore } from "react";

/** Whether the media query matches now; the component re-renders when that changes. */
export function useMediaQuery(query: string): boolean {
  return useSyncExternalStore(
    (onChange) => {
      const mq = window.matchMedia(query);
      mq.addEventListener("change", onChange);
      return () => {
        mq.removeEventListener("change", onChange);
      };
    },
    () => window.matchMedia(query).matches,
  );
}
