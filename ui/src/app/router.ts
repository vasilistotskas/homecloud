import { createHashHistory, createRootRoute, createRoute, createRouter, Outlet } from "@tanstack/react-router";

import { ActivityPage } from "../pages/Activity";
import { ConnectPage } from "../pages/Connect";
import { OverviewPage } from "../pages/Overview";
import { QuarantinePage } from "../pages/Quarantine";
import { SettingsPage } from "../pages/Settings";
import { Shell } from "./Shell";

const rootRoute = createRootRoute({ component: Outlet });

const shellRoute = createRoute({ getParentRoute: () => rootRoute, id: "shell", component: Shell });

const overviewRoute = createRoute({ getParentRoute: () => shellRoute, path: "/", component: OverviewPage });
const activityRoute = createRoute({ getParentRoute: () => shellRoute, path: "activity", component: ActivityPage });
const quarantineRoute = createRoute({
  getParentRoute: () => shellRoute,
  path: "quarantine",
  component: QuarantinePage,
});
const settingsRoute = createRoute({ getParentRoute: () => shellRoute, path: "settings", component: SettingsPage });

export const connectRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "connect",
  component: ConnectPage,
  validateSearch: (search: Record<string, unknown>): { code?: string } =>
    typeof search.code === "string" ? { code: search.code } : {},
});

const routeTree = rootRoute.addChildren([
  shellRoute.addChildren([overviewRoute, activityRoute, quarantineRoute, settingsRoute]),
  connectRoute,
]);

// createAppRouter builds the app's router. Hash history works the same when the
// agent serves the app and inside the desktop shell, without server-side rewrites.
export function createAppRouter() {
  return createRouter({ routeTree, history: createHashHistory() });
}

declare module "@tanstack/react-router" {
  interface Register {
    router: ReturnType<typeof createAppRouter>;
  }
}
