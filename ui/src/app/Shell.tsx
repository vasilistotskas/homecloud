import {
  CounterBadge,
  makeStyles,
  NavDrawer,
  NavDrawerBody,
  NavDrawerFooter,
  NavDrawerHeader,
  NavItem,
  Spinner,
  Text,
  Toaster,
  tokens,
} from "@fluentui/react-components";
import {
  CloudRegular,
  HistoryRegular,
  HomeRegular,
  ImageMultipleRegular,
  SearchRegular,
  SettingsRegular,
  ShieldErrorRegular,
} from "@fluentui/react-icons";
import { useEffect, useRef, useState, type ReactNode } from "react";

import { useOverview, type Connection } from "../api/overviewContext";
import { CommandPalette } from "../components/CommandPalette";
import { toasterId } from "../components/toast";
import { Phase } from "../gen/homecloud/agent/v3/agent_pb";
import { isPath, navigate, useLocation } from "./router";

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
  searchTrigger: {
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    width: "100%",
    padding: `${tokens.spacingVerticalXS} ${tokens.spacingHorizontalM}`,
    marginTop: tokens.spacingVerticalS,
    borderRadius: tokens.borderRadiusMedium,
    border: `1px solid ${tokens.colorNeutralStroke2}`,
    backgroundColor: tokens.colorNeutralBackground3,
    color: tokens.colorNeutralForeground3,
    cursor: "pointer",
    fontSize: tokens.fontSizeBase200,
    ":hover": {
      backgroundColor: tokens.colorNeutralBackground3Hover,
      color: tokens.colorNeutralForeground1,
    },
  },
  searchIconLabel: {
    display: "flex",
    alignItems: "center",
    gap: tokens.spacingHorizontalXS,
  },
  kbdPill: {
    padding: "1px 6px",
    borderRadius: tokens.borderRadiusSmall,
    backgroundColor: tokens.colorNeutralBackground1,
    border: `1px solid ${tokens.colorNeutralStroke1}`,
    fontFamily: tokens.fontFamilyMonospace,
    fontSize: tokens.fontSizeBase100,
    color: tokens.colorNeutralForeground2,
  },
  syncPill: {
    marginLeft: "auto",
    display: "flex",
    alignItems: "center",
  },
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

const connectionStatus: Record<Connection, { label: string; color: string }> = {
  connecting: { label: "Connecting…", color: tokens.colorNeutralForeground3 },
  live: { label: "Agent connected", color: tokens.colorPaletteGreenForeground1 },
  reconnecting: { label: "Agent not reachable", color: tokens.colorPaletteRedForeground1 },
  unauthenticated: { label: "Not signed in", color: tokens.colorPaletteRedForeground1 },
  "archive-unavailable": { label: "Archive drive unavailable", color: tokens.colorPaletteDarkOrangeForeground1 },
};

const pages = [
  { path: "/", label: "Overview", icon: <HomeRegular /> },
  { path: "/library", label: "Library", icon: <ImageMultipleRegular /> },
  { path: "/activity", label: "Activity", icon: <HistoryRegular /> },
  { path: "/quarantine", label: "Quarantine", icon: <ShieldErrorRegular /> },
  { path: "/settings", label: "Settings", icon: <SettingsRegular /> },
] as const;

/** The app layout: navigation, connection status and the page. */
export function Shell({ children }: { children: ReactNode }) {
  const styles = useStyles();
  const { path } = useLocation();
  // Every page starts at its top, also after back or forward: pages are
  // dashboards and lists that reload their data, so a restored offset would
  // point at different content. Wide windows scroll the content pane, narrow
  // ones the document; the browser's own restoration is turned off so it
  // does not restore one of them in between.
  const mainRef = useRef<HTMLElement>(null);
  useEffect(() => {
    window.history.scrollRestoration = "manual";
  }, []);
  useEffect(() => {
    if (mainRef.current !== null) {
      mainRef.current.scrollTop = 0;
    }
    window.scrollTo(0, 0);
  }, [path]);
  const { overview, connection } = useOverview();
  const [paletteOpen, setPaletteOpen] = useState(false);

  useEffect(() => {
    function onKeyDown(e: KeyboardEvent) {
      if ((e.ctrlKey || e.metaKey) && (e.key === "k" || e.key === "K")) {
        e.preventDefault();
        setPaletteOpen((open) => !open);
      }
    }
    window.addEventListener("keydown", onKeyDown);
    return () => {
      window.removeEventListener("keydown", onKeyDown);
    };
  }, []);

  // A browser without a session is sent to pair once, when the agent rejects it.
  useEffect(() => {
    if (connection === "unauthenticated") {
      navigate("/connect");
    }
  }, [connection]);

  const quarantined = Number(overview?.assets?.quarantined ?? 0n);
  const status = connectionStatus[connection];
  const phase = overview?.activity?.phase ?? Phase.UNSPECIFIED;
  const isSyncing =
    phase === Phase.DOWNLOADING ||
    phase === Phase.INGESTING ||
    phase === Phase.VERIFYING ||
    phase === Phase.PREPARING_PREVIEWS;

  return (
    <div className={styles.root}>
      <NavDrawer
        open
        type="inline"
        className={styles.nav}
        selectedValue={path ?? "/"}
        onNavItemSelect={(_, data) => {
          if (isPath(data.value)) {
            navigate(data.value);
          }
        }}
      >
        <NavDrawerHeader>
          <div className={styles.brand}>
            <CloudRegular className={styles.brandIcon} aria-hidden />
            <Text weight="semibold" size={400}>
              homecloud
            </Text>
          </div>
          <button
            type="button"
            className={styles.searchTrigger}
            onClick={() => {
              setPaletteOpen(true);
            }}
            aria-label="Open command palette"
          >
            <span className={styles.searchIconLabel}>
              <SearchRegular /> Search
            </span>
            <kbd className={styles.kbdPill}>Ctrl+K</kbd>
          </button>
        </NavDrawerHeader>
        <NavDrawerBody>
          {pages.map((p) => (
            <NavItem key={p.path} value={p.path} icon={p.icon}>
              {p.label}
              {p.path === "/activity" && isSyncing && (
                <span className={styles.syncPill}>
                  <Spinner size="extra-tiny" />
                </span>
              )}
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
            <span className={styles.dot} style={{ backgroundColor: status.color }} />
            <Text size={200}>{status.label}</Text>
          </div>
        </NavDrawerFooter>
      </NavDrawer>
      <main ref={mainRef} className={styles.main}>
        {children}
      </main>
      <CommandPalette open={paletteOpen} onOpenChange={setPaletteOpen} />
      <Toaster toasterId={toasterId} position="bottom-end" />
    </div>
  );
}
