import {
  Button,
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
import { CloudRegular, SearchRegular } from "@fluentui/react-icons";
import { useEffect, useRef, useState, type ReactNode } from "react";

import { useOverview, type Connection } from "../api/overviewContext";
import { cyclePhases } from "../api/sync";
import { CommandPalette } from "../components/CommandPalette";
import { toasterId } from "../components/toast";
import { Phase } from "../gen/homecloud/agent/v3/agent_pb";
import { wideWindow } from "./breakpoints";
import { navPages } from "./navPages";
import { isPath, navigate, useLocation } from "./router";

const useStyles = makeStyles({
  // Narrow windows stack the navigation above the content and scroll as a page.
  // Wide windows are an app layout: the window never scrolls, the navigation is
  // a fixed full-height pane and only the content pane scrolls.
  root: {
    display: "flex",
    flexWrap: "wrap",
    minHeight: "100vh",
    [wideWindow]: { flexWrap: "nowrap", height: "100vh", overflow: "hidden" },
  },
  nav: {
    flex: "1 1 100%",
    minHeight: "auto",
    [wideWindow]: { flex: "0 0 248px", height: "100%" },
  },
  brand: { display: "flex", alignItems: "center", gap: tokens.spacingHorizontalS },
  brandIcon: { fontSize: "24px", color: tokens.colorBrandForeground1 },
  search: { width: "100%", marginTop: tokens.spacingVerticalS, justifyContent: "flex-start" },
  shortcut: {
    marginLeft: "auto",
    padding: `0 ${tokens.spacingHorizontalXS}`,
    borderRadius: tokens.borderRadiusSmall,
    border: `${tokens.strokeWidthThin} solid ${tokens.colorNeutralStroke1}`,
    fontFamily: tokens.fontFamilyMonospace,
    fontSize: tokens.fontSizeBase100,
    color: tokens.colorNeutralForeground3,
  },
  main: {
    flex: "999 1 560px",
    minWidth: 0,
    boxSizing: "border-box",
    padding: `${tokens.spacingVerticalXXL} ${tokens.spacingHorizontalXXXL}`,
    [wideWindow]: { height: "100%", overflowY: "auto" },
  },
  footer: { display: "flex", alignItems: "center", gap: tokens.spacingHorizontalS, padding: tokens.spacingHorizontalM },
  dot: { width: "8px", height: "8px", borderRadius: tokens.borderRadiusCircular, flex: "none" },
  // The sync spinner and the quarantine count sit at the item's end.
  badge: { marginLeft: "auto" },
});

const connectionStatus: Record<Connection, { label: string; color: string }> = {
  connecting: { label: "Connecting…", color: tokens.colorNeutralForeground3 },
  live: { label: "Agent connected", color: tokens.colorPaletteGreenForeground1 },
  reconnecting: { label: "Agent not reachable", color: tokens.colorPaletteRedForeground1 },
  unauthenticated: { label: "Not signed in", color: tokens.colorPaletteRedForeground1 },
  "archive-unavailable": { label: "Archive drive unavailable", color: tokens.colorPaletteDarkOrangeForeground1 },
};

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

  // Ctrl+K (Cmd+K on macOS) opens the command palette from anywhere.
  useEffect(() => {
    function onKeyDown(e: KeyboardEvent) {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "k") {
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
  const syncing = cyclePhases.has(overview?.activity?.phase ?? Phase.UNSPECIFIED);

  return (
    <div className={styles.root}>
      <NavDrawer
        open
        type="inline"
        className={styles.nav}
        selectedValue={path ?? ""}
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
          <Button
            className={styles.search}
            icon={<SearchRegular />}
            aria-keyshortcuts="Control+K"
            onClick={() => {
              setPaletteOpen(true);
            }}
          >
            Search
            <kbd className={styles.shortcut}>Ctrl+K</kbd>
          </Button>
        </NavDrawerHeader>
        <NavDrawerBody>
          {navPages.map((p) => (
            <NavItem key={p.path} value={p.path} icon={p.icon}>
              {p.label}
              {p.path === "/activity" && syncing && (
                <Spinner className={styles.badge} size="extra-tiny" aria-label="Sync running" />
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
