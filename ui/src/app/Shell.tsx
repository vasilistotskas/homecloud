import {
  Button,
  CounterBadge,
  Hamburger,
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
import { narrowQuery, narrowWindow, wideWindow } from "./breakpoints";
import { navPages } from "./navPages";
import { isPath, navigate, useLocation } from "./router";
import { useMediaQuery } from "./useMediaQuery";

const useStyles = makeStyles({
  // Narrow windows (phones) scroll as a page under a top bar whose menu button
  // opens the navigation over it. Wide windows are an app layout: the window
  // never scrolls, the navigation is a fixed full-height pane and only the
  // content pane scrolls.
  root: {
    minHeight: "100vh",
    [wideWindow]: { display: "flex", height: "100vh", overflow: "hidden" },
  },
  nav: { [wideWindow]: { flex: "0 0 248px", height: "100%" } },
  topBar: {
    position: "sticky",
    top: 0,
    zIndex: 1,
    display: "flex",
    alignItems: "center",
    gap: tokens.spacingHorizontalS,
    padding: `${tokens.spacingVerticalS} ${tokens.spacingHorizontalS}`,
    backgroundColor: tokens.colorNeutralBackground1,
    borderBottom: `${tokens.strokeWidthThin} solid ${tokens.colorNeutralStroke2}`,
  },
  topStatus: { marginLeft: "auto", display: "flex", alignItems: "center" },
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
    [wideWindow]: { flex: "1 1 auto", height: "100%", overflowY: "auto" },
    [narrowWindow]: { padding: `${tokens.spacingVerticalL} ${tokens.spacingHorizontalL}` },
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
  const narrow = useMediaQuery(narrowQuery);
  const [menuOpen, setMenuOpen] = useState(false);

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

  const brand = (
    <div className={styles.brand}>
      <CloudRegular className={styles.brandIcon} aria-hidden />
      <Text weight="semibold" size={400}>
        homecloud
      </Text>
    </div>
  );
  const statusLine = (
    <div className={styles.footer} role="status">
      <span className={styles.dot} style={{ backgroundColor: status.color }} />
      <Text size={200}>{status.label}</Text>
    </div>
  );

  return (
    <div className={styles.root}>
      {narrow && (
        <header className={styles.topBar}>
          <Hamburger
            aria-label="Menu"
            aria-expanded={menuOpen}
            onClick={() => {
              setMenuOpen(true);
            }}
          />
          {brand}
          <span className={styles.topStatus} title={status.label}>
            <span
              className={styles.dot}
              style={{ backgroundColor: status.color }}
              aria-label={status.label}
              role="img"
            />
          </span>
        </header>
      )}
      <NavDrawer
        open={!narrow || menuOpen}
        type={narrow ? "overlay" : "inline"}
        onOpenChange={(_, data) => {
          setMenuOpen(data.open);
        }}
        className={styles.nav}
        selectedValue={path ?? ""}
        onNavItemSelect={(_, data) => {
          setMenuOpen(false);
          if (isPath(data.value)) {
            navigate(data.value);
          }
        }}
      >
        <NavDrawerHeader>
          {brand}
          <Button
            className={styles.search}
            icon={<SearchRegular />}
            aria-keyshortcuts="Control+K"
            onClick={() => {
              setMenuOpen(false);
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
        <NavDrawerFooter>{statusLine}</NavDrawerFooter>
      </NavDrawer>
      <main ref={mainRef} className={styles.main}>
        {children}
      </main>
      <CommandPalette open={paletteOpen} onOpenChange={setPaletteOpen} />
      <Toaster toasterId={toasterId} position="bottom-end" />
    </div>
  );
}
