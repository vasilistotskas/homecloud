import { Dialog, DialogSurface, makeStyles, mergeClasses, SearchBox, Text, tokens } from "@fluentui/react-components";
import {
  ArrowSyncRegular,
  DarkThemeRegular,
  HistoryRegular,
  WeatherMoonRegular,
  WeatherSunnyRegular,
} from "@fluentui/react-icons";
import { useId, useState, type KeyboardEvent, type ReactElement } from "react";

import { useStartSync } from "../api/sync";
import { navPages } from "../app/navPages";
import { navigate } from "../app/router";
import { useThemePreference, type ThemePreference } from "../app/themeContext";
import { activityTabs, type ActivityTab } from "../pages/activityTabs";

const useStyles = makeStyles({
  surface: { maxWidth: "560px", padding: tokens.spacingVerticalM },
  body: { display: "flex", flexDirection: "column", gap: tokens.spacingVerticalS },
  list: {
    maxHeight: "360px",
    overflowY: "auto",
    display: "flex",
    flexDirection: "column",
    gap: tokens.spacingVerticalXXS,
  },
  option: {
    display: "flex",
    alignItems: "center",
    gap: tokens.spacingHorizontalM,
    padding: `${tokens.spacingVerticalS} ${tokens.spacingHorizontalM}`,
    borderRadius: tokens.borderRadiusMedium,
    cursor: "pointer",
    userSelect: "none",
  },
  active: { backgroundColor: tokens.colorSubtleBackgroundSelected },
  icon: { display: "flex", fontSize: tokens.fontSizeBase500, color: tokens.colorNeutralForeground2 },
  empty: { padding: tokens.spacingVerticalL, textAlign: "center", color: tokens.colorNeutralForeground3 },
});

interface Command {
  id: string;
  title: string;
  icon: ReactElement;
  /** Other words the command is found by. */
  keywords: readonly string[];
  perform: () => void;
}

const themeCommands: Record<ThemePreference, { title: string; icon: ReactElement }> = {
  system: { title: "Theme: follow the system", icon: <DarkThemeRegular /> },
  light: { title: "Theme: light", icon: <WeatherSunnyRegular /> },
  dark: { title: "Theme: dark", icon: <WeatherMoonRegular /> },
};

/** Ctrl+K: go to any page or Activity tab, start a sync or change the theme. */
export function CommandPalette({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const styles = useStyles();
  return (
    <Dialog
      open={open}
      onOpenChange={(_, data) => {
        onOpenChange(data.open);
      }}
    >
      <DialogSurface className={styles.surface} aria-label="Command palette">
        {/* The surface mounts on open, so every opening starts with an empty query. */}
        <Commands
          close={() => {
            onOpenChange(false);
          }}
        />
      </DialogSurface>
    </Dialog>
  );
}

/** The command search, following the ARIA combobox pattern: focus stays in the input. */
function Commands({ close }: { close: () => void }) {
  const styles = useStyles();
  const sync = useStartSync();
  const { preference, setPreference } = useThemePreference();
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const listId = useId();

  const commands: Command[] = [
    ...navPages.map((p) => ({
      id: `page:${p.path}`,
      title: p.label,
      icon: p.icon,
      keywords: [],
      perform: () => {
        navigate(p.path);
      },
    })),
    ...(Object.entries(activityTabs) as [ActivityTab, string][]).map(([tab, label]) => ({
      id: `activity:${tab}`,
      title: `Activity: ${label}`,
      icon: <HistoryRegular />,
      keywords: ["history"],
      perform: () => {
        navigate("/activity", { search: { tab } });
      },
    })),
    ...(sync.canStart
      ? [{ id: "sync", title: "Sync now", icon: <ArrowSyncRegular />, keywords: ["start", "run"], perform: sync.start }]
      : []),
    ...(Object.keys(themeCommands) as ThemePreference[])
      .filter((p) => p !== preference)
      .map((p) => ({
        id: `theme:${p}`,
        ...themeCommands[p],
        keywords: ["appearance", "mode"],
        perform: () => {
          setPreference(p);
        },
      })),
  ];

  const q = query.trim().toLowerCase();
  const matches =
    q === "" ? commands : commands.filter((c) => [c.title, ...c.keywords].some((w) => w.toLowerCase().includes(q)));
  const current = Math.min(active, matches.length - 1);
  const optionId = (i: number) => `${listId}-${i}`;

  const run = (c: Command) => {
    close();
    c.perform();
  };

  const onKeyDown = (e: KeyboardEvent) => {
    const last = matches.length - 1;
    const target: Partial<Record<string, number>> = {
      ArrowDown: current < last ? current + 1 : 0,
      ArrowUp: current > 0 ? current - 1 : last,
      Home: 0,
      End: last,
    };
    const next = target[e.key];
    if (next !== undefined && matches.length > 0) {
      e.preventDefault();
      setActive(next);
      document.getElementById(optionId(next))?.scrollIntoView({ block: "nearest" });
    } else if (e.key === "Enter") {
      const c = matches[current];
      if (c !== undefined) {
        e.preventDefault();
        run(c);
      }
    }
  };

  return (
    <div className={styles.body}>
      <SearchBox
        placeholder="Go to a page or run a command…"
        value={query}
        role="combobox"
        aria-label="Command"
        aria-expanded
        aria-controls={listId}
        aria-autocomplete="list"
        aria-activedescendant={current >= 0 ? optionId(current) : undefined}
        onChange={(_, d) => {
          setQuery(d.value);
          setActive(0);
        }}
        onKeyDown={onKeyDown}
      />
      <div id={listId} className={styles.list} role="listbox" aria-label="Commands">
        {matches.map((c, i) => (
          <div
            key={c.id}
            id={optionId(i)}
            role="option"
            aria-selected={i === current}
            className={mergeClasses(styles.option, i === current && styles.active)}
            onClick={() => {
              run(c);
            }}
            onMouseMove={() => {
              setActive(i);
            }}
          >
            <span className={styles.icon} aria-hidden>
              {c.icon}
            </span>
            <Text>{c.title}</Text>
          </div>
        ))}
      </div>
      {matches.length === 0 && <Text className={styles.empty}>No page or command matches.</Text>}
    </div>
  );
}
