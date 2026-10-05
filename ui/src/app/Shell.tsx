import {
  CounterBadge,
  makeStyles,
  NavDrawer,
  NavDrawerBody,
  NavDrawerFooter,
  NavDrawerHeader,
  NavItem,
  Text,
  Toaster,
  tokens,
} from "@fluentui/react-components";
import {
  CloudRegular,
  HistoryRegular,
  HomeRegular,
  ImageMultipleRegular,
  SettingsRegular,
  ShieldErrorRegular,
} from "@fluentui/react-icons";
import { Outlet, useLocation, useNavigate } from "@tanstack/react-router";
import { useEffect, useRef } from "react";

import { useOverview } from "../api/overviewContext";
import { toasterId } from "../components/toast";

const useStyles = makeStyles({
  // Narrow windows stack the navigation above the content and scroll as a page.
  // Wide windows are an app layout: the window never scrolls, the navigation is
  // a fixed full-height pane and only the content pane scrolls.
  root: {
    display: "flex",
    flexWrap: "wrap",
    minHeight: "100vh",
    "@media (min-width: 840px)": { flexWrap: "nowrap", height: "100vh", overflow: "hidden" },
  },
  nav: {
    flex: "1 1 100%",
    minHeight: "auto",
    "@media (min-width: 840px)": { flex: "0 0 248px", height: "100%" },
  },
  brand: { display: "flex", alignItems: "center", gap: tokens.spacingHorizontalS },
  brandIcon: { fontSize: "24px", color: tokens.colorBrandForeground1 },
  main: {
    flex: "999 1 560px",
    minWidth: 0,
    boxSizing: "border-box",
    padding: `${tokens.spacingVerticalXXL} ${tokens.spacingHorizontalXXXL}`,
    "@media (min-width: 840px)": { height: "100%", overflowY: "auto" },
  },
  footer: { display: "flex", alignItems: "center", gap: tokens.spacingHorizontalS, padding: tokens.spacingHorizontalM },
  dot: { width: "8px", height: "8px", borderRadius: tokens.borderRadiusCircular, flex: "none" },
  badge: { marginLeft: "auto" },
});

const pages = [
  { path: "/", label: "Overview", icon: <HomeRegular /> },
  { path: "/library", label: "Library", icon: <ImageMultipleRegular /> },
  { path: "/activity", label: "Activity", icon: <HistoryRegular /> },
  { path: "/quarantine", label: "Quarantine", icon: <ShieldErrorRegular /> },
  { path: "/settings", label: "Settings", icon: <SettingsRegular /> },
] as const;

export function Shell() {
  const styles = useStyles();
  const navigate = useNavigate();
  const { pathname } = useLocation();
  // The content pane is the scroller in the app layout: each page starts at its top.
  const mainRef = useRef<HTMLElement>(null);
  useEffect(() => {
    if (mainRef.current !== null) {
      mainRef.current.scrollTop = 0;
    }
  }, [pathname]);
  const { overview, connection } = useOverview();

  // A browser without a session is sent to pair once, when the agent rejects it.
  useEffect(() => {
    if (connection === "unauthenticated") {
      void navigate({ to: "/connect" });
    }
  }, [connection, navigate]);

  const quarantined = Number(overview?.assets?.quarantined ?? 0n);
  const live = connection === "live";

  return (
    <div className={styles.root}>
      <NavDrawer
        open
        type="inline"
        className={styles.nav}
        selectedValue={pathname}
        onNavItemSelect={(_, data) => {
          void navigate({ to: data.value });
        }}
      >
        <NavDrawerHeader>
          <div className={styles.brand}>
            <CloudRegular className={styles.brandIcon} aria-hidden />
            <Text weight="semibold" size={400}>
              homecloud
            </Text>
          </div>
        </NavDrawerHeader>
        <NavDrawerBody>
          {pages.map((p) => (
            <NavItem key={p.path} value={p.path} icon={p.icon}>
              {p.label}
              {p.path === "/quarantine" && quarantined > 0 && (
                <CounterBadge
                  className={styles.badge}
                  count={quarantined}
                  color="danger"
                  size="small"
                  aria-label={`${quarantined} quarantined`}
                />
              )}
            </NavItem>
          ))}
        </NavDrawerBody>
        <NavDrawerFooter>
          <div className={styles.footer} role="status">
            <span
              className={styles.dot}
              style={{
                backgroundColor: live ? tokens.colorPaletteGreenForeground1 : tokens.colorPaletteRedForeground1,
              }}
            />
            <Text size={200}>{live ? "Agent connected" : "Agent not reachable"}</Text>
          </div>
        </NavDrawerFooter>
      </NavDrawer>
      <main ref={mainRef} className={styles.main}>
        <Outlet />
      </main>
      <Toaster toasterId={toasterId} position="bottom-end" />
    </div>
  );
}
