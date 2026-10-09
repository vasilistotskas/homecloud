import { useEffect, useState } from "react";

/** How long typing must pause before a search runs. */
export const searchDelayMs = 300;

/** value, once it has stopped changing for delayMs. */
export function useDebounced<T>(value: T, delayMs: number): T {
  const [settled, setSettled] = useState(value);
  useEffect(() => {
    const timer = setTimeout(() => {
      setSettled(value);
    }, delayMs);
    return () => {
      clearTimeout(timer);
    };
  }, [value, delayMs]);
  return settled;
}
