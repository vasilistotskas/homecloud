import type { Theme } from "@fluentui/react-components";
import { createContext, use } from "react";

export type ThemePreference = "system" | "light" | "dark";

export interface ThemeState {
  preference: ThemePreference;
  setPreference: (p: ThemePreference) => void;
  theme: Theme;
}

export const ThemeContext = createContext<ThemeState | undefined>(undefined);

export function useThemePreference(): ThemeState {
  const ctx = use(ThemeContext);
  if (ctx === undefined) {
    throw new Error("useThemePreference must be used inside ThemeStateProvider");
  }
  return ctx;
}
