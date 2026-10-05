import { webDarkTheme, webLightTheme, type Theme } from "@fluentui/react-components";
import { useState, useSyncExternalStore, type ReactNode } from "react";

import { ThemeContext, type ThemePreference, type ThemeState } from "./themeContext";

const storageKey = "homecloud.theme";
const darkQuery = "(prefers-color-scheme: dark)";

function readPreference(): ThemePreference {
  try {
    const v = localStorage.getItem(storageKey);
    return v === "light" || v === "dark" ? v : "system";
  } catch {
    return "system";
  }
}

function writePreference(p: ThemePreference): void {
  try {
    if (p === "system") {
      localStorage.removeItem(storageKey);
    } else {
      localStorage.setItem(storageKey, p);
    }
  } catch {
    // Storage can be unavailable (private windows); the choice then lasts for this session only.
  }
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
