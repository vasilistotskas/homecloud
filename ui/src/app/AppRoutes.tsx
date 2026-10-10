import { useEffect, type ReactNode } from "react";

import { ActivityPage } from "../pages/Activity";
import { ConnectPage } from "../pages/Connect";
import { LibraryPage } from "../pages/Library";
import { OverviewPage } from "../pages/Overview";
import { QuarantinePage } from "../pages/Quarantine";
import { SafeToDeletePage } from "../pages/SafeToDelete";
import { SettingsPage } from "../pages/Settings";
import { navigate, useLocation, type Path } from "./router";
import { Shell } from "./Shell";

/** The pages inside the app's shell (navigation and status); /connect stands alone. */
const shellPages: Record<Exclude<Path, "/connect">, () => ReactNode> = {
  "/": () => <OverviewPage />,
  "/library": () => <LibraryPage />,
  "/activity": () => <ActivityPage />,
  "/quarantine": () => <QuarantinePage />,
  "/safe-to-delete": () => <SafeToDeletePage />,
  "/settings": () => <SettingsPage />,
};

/** Renders the page the location names. */
export function AppRoutes() {
  const { path } = useLocation();

  // A hash that names no page (an old or mistyped link) shows the overview,
  // and the address is corrected without adding a history entry.
  useEffect(() => {
    if (path === undefined) {
      navigate("/", { replace: true });
    }
  }, [path]);

  if (path === "/connect") {
    return <ConnectPage />;
  }
  return <Shell>{shellPages[path ?? "/"]()}</Shell>;
}
