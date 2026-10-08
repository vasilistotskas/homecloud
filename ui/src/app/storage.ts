/** Keys of the view preferences this app keeps in the browser's local storage. */
export const storageKeys = {
  theme: "homecloud.theme",
  gridDensity: "homecloud.gridDensity",
} as const;

type StorageKey = (typeof storageKeys)[keyof typeof storageKeys];

/**
 * The stored value under key when isValid accepts it. Storage can be
 * unavailable (private windows, blocked site data): the preference then
 * falls back to its default.
 */
export function readStored<T extends string>(key: StorageKey, isValid: (v: string) => v is T): T | undefined {
  try {
    const v = localStorage.getItem(key);
    return v !== null && isValid(v) ? v : undefined;
  } catch {
    return undefined;
  }
}

/** Stores value under key, or removes the key when value is undefined. */
export function writeStored(key: StorageKey, value: string | undefined): void {
  try {
    if (value === undefined) {
      localStorage.removeItem(key);
    } else {
      localStorage.setItem(key, value);
    }
  } catch {
    // Storage can be unavailable; the choice then lasts for this session only.
  }
}
