import { webDarkTheme, webLightTheme, type Theme } from "@fluentui/react-components";
import { useState, useSyncExternalStore, type ReactNode } from "react";

import { readStored, storageKeys, writeStored } from "./storage";
import { ThemeContext, type ThemePreference, type ThemeState } from "./themeContext";

const darkQuery = "(prefers-color-scheme: dark)";

function isStoredPreference(v: string): v is "light" | "dark" {
  return v === "light" || v === "dark";
}

function readPreference(): ThemePreference {
  return readStored(storageKeys.theme, isStoredPreference) ?? "system";
}

function writePreference(p: ThemePreference): void {
  writeStored(storageKeys.theme, p === "system" ? undefined : p);
}

function subscribeSystem(onChange: () => void): () => void {
  const mq = window.matchMedia(darkQuery);
  mq.addEventListener("change", onChange);
  return () => {
    mq.removeEventListener("change", onChange);
  };
}

function systemIsDark(): boolean {
  return window.matchMedia(darkQuery).matches;
}

/** Follows the OS light/dark setting unless the user picked one in Settings. */
export function ThemeStateProvider({ children }: { children: (theme: Theme) => ReactNode }) {
  const [preference, setPref] = useState(readPreference);
  const dark = useSyncExternalStore(subscribeSystem, systemIsDark);
  const theme = preference === "dark" || (preference === "system" && dark) ? webDarkTheme : webLightTheme;
  const value: ThemeState = {
    preference,
    theme,
    setPreference: (p) => {
      writePreference(p);
      setPref(p);
    },
  };
  return <ThemeContext value={value}>{children(theme)}</ThemeContext>;
}
