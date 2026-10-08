import {
  HistoryRegular,
  HomeRegular,
  ImageMultipleRegular,
  SettingsRegular,
  ShieldErrorRegular,
} from "@fluentui/react-icons";
import type { ReactElement } from "react";

import type { Path } from "./router";

/** The pages in the navigation, in order; the command palette lists the same. */
export const navPages: readonly { path: Path; label: string; icon: ReactElement }[] = [
  { path: "/", label: "Overview", icon: <HomeRegular /> },
  { path: "/library", label: "Library", icon: <ImageMultipleRegular /> },
  { path: "/activity", label: "Activity", icon: <HistoryRegular /> },
  { path: "/quarantine", label: "Quarantine", icon: <ShieldErrorRegular /> },
  { path: "/settings", label: "Settings", icon: <SettingsRegular /> },
];
