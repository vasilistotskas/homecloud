import { useMutation } from "@connectrpc/connect-query";
import {
  Dialog,
  DialogBody,
  DialogSurface,
  makeStyles,
  mergeClasses,
  SearchBox,
  Text,
  tokens,
} from "@fluentui/react-components";
import {
  CloudRegular,
  HistoryRegular,
  HomeRegular,
  ImageMultipleRegular,
  PlayRegular,
  SettingsRegular,
  ShieldErrorRegular,
  WeatherMoonRegular,
  WeatherSunnyRegular,
} from "@fluentui/react-icons";
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";

import { useOverview } from "../api/overviewContext";
import { navigate } from "../app/router";
import { useThemePreference } from "../app/themeContext";
import { useNotify } from "./toast";
import { AgentService, Phase } from "../gen/homecloud/agent/v3/agent_pb";

const useStyles = makeStyles({
  surface: {
    maxWidth: "560px",
    width: "100%",
    borderRadius: tokens.borderRadiusLarge,
    padding: tokens.spacingVerticalM,
    boxShadow: tokens.shadow64,
  },
  body: {
    display: "flex",
    flexDirection: "column",
    gap: tokens.spacingVerticalM,
    padding: 0,
  },
  list: {
    maxHeight: "360px",
    overflowY: "auto",
    display: "flex",
    flexDirection: "column",
    gap: tokens.spacingVerticalXXS,
  },
  groupHeader: {
    padding: `${tokens.spacingVerticalXS} ${tokens.spacingHorizontalM}`,
    color: tokens.colorNeutralForeground3,
    fontSize: tokens.fontSizeBase200,
    fontWeight: tokens.fontWeightSemibold,
    textTransform: "uppercase",
    letterSpacing: "0.5px",
  },
  item: {
    display: "flex",
    alignItems: "center",
    gap: tokens.spacingHorizontalM,
    padding: `${tokens.spacingVerticalS} ${tokens.spacingHorizontalM}`,
    borderRadius: tokens.borderRadiusMedium,
    cursor: "pointer",
    userSelect: "none",
    border: "none",
    backgroundColor: "transparent",
    textAlign: "left",
    width: "100%",
    boxSizing: "border-box",
    transition: "background-color 0.1s ease-in-out",
    ":hover": {
      backgroundColor: tokens.colorSubtleBackgroundHover,
    },
  },
  itemActive: {
    backgroundColor: tokens.colorSubtleBackgroundSelected,
    outline: `2px solid ${tokens.colorBrandStroke1}`,
  },
  icon: {
    fontSize: "20px",
    display: "flex",
    alignItems: "center",
    color: tokens.colorNeutralForeground2,
  },
  textContainer: {
    display: "flex",
    flexDirection: "column",
    flex: 1,
    minWidth: 0,
  },
  title: {
    fontWeight: tokens.fontWeightRegular,
    color: tokens.colorNeutralForeground1,
  },
  subtitle: {
    fontSize: tokens.fontSizeBase200,
    color: tokens.colorNeutralForeground3,
  },
  empty: {
    padding: tokens.spacingVerticalL,
    textAlign: "center",
    color: tokens.colorNeutralForeground3,
  },
  footer: {
    display: "flex",
    justifyContent: "space-between",
    alignItems: "center",
    borderTop: `1px solid ${tokens.colorNeutralStroke2}`,
    paddingTop: tokens.spacingVerticalS,
    paddingLeft: tokens.spacingHorizontalM,
    paddingRight: tokens.spacingHorizontalM,
    color: tokens.colorNeutralForeground4,
    fontSize: tokens.fontSizeBase100,
  },
  kbd: {
    padding: "2px 6px",
    borderRadius: tokens.borderRadiusSmall,
    backgroundColor: tokens.colorNeutralBackground3,
    border: `1px solid ${tokens.colorNeutralStroke1}`,
    fontFamily: tokens.fontFamilyMonospace,
    fontSize: tokens.fontSizeBase100,
  },
});

interface Command {
  id: string;
  title: string;
  subtitle?: string;
  category: "Navigation" | "Actions";
  icon: ReactNode;
  perform: () => void;
  keywords?: string[];
}

export function CommandPalette({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const styles = useStyles();
  const notify = useNotify();
  const { overview } = useOverview();
  const { preference, setPreference } = useThemePreference();
  const [query, setQuery] = useState("");
  const [activeIndex, setActiveIndex] = useState(0);
  const [prevOpen, setPrevOpen] = useState(open);
  const inputRef = useRef<HTMLInputElement>(null);

  if (open !== prevOpen) {
    setPrevOpen(open);
    if (open) {
      setQuery("");
      setActiveIndex(0);
    }
  }

  const phase = overview?.activity?.phase ?? Phase.UNSPECIFIED;
  const startSync = useMutation(AgentService.method.startSync, {
    onSuccess: (res) => {
      if (!res.started) {
        notify("info", "A sync is already running or queued");
      } else if (phase !== Phase.IDLE) {
        notify("info", "Sync queued: it starts as soon as current job finishes");
      } else {
        notify("success", "Sync started");
      }
    },
    onError: (err) => {
      notify("error", "Couldn't start sync", err.message);
    },
  });

  const commands = useMemo<Command[]>(() => {
    return [
      {
        id: "nav-overview",
        title: "Overview",
        subtitle: "System status and dashboard",
        category: "Navigation",
        icon: <HomeRegular />,
        keywords: ["dashboard", "home", "stats", "storage"],
        perform: () => {
          navigate("/");
        },
      },
      {
        id: "nav-library",
        title: "Library",
        subtitle: "Browse photos, videos, and live photos",
        category: "Navigation",
        icon: <ImageMultipleRegular />,
        keywords: ["photos", "videos", "media", "gallery", "images"],
        perform: () => {
          navigate("/library");
        },
      },
      {
        id: "nav-activity-runs",
        title: "Activity: Syncs",
        subtitle: "View cycle synchronization history",
        category: "Navigation",
        icon: <HistoryRegular />,
        keywords: ["history", "cycles", "syncs", "download"],
        perform: () => {
          navigate("/activity", { search: { tab: "runs" } });
        },
      },
      {
        id: "nav-activity-integrity",
        title: "Activity: Integrity checks",
        subtitle: "View background scrub and bit-rot checks",
        category: "Navigation",
        icon: <HistoryRegular />,
        keywords: ["scrub", "checksum", "integrity", "verify", "re-read"],
        perform: () => {
          navigate("/activity", { search: { tab: "integrity" } });
        },
      },
      {
        id: "nav-activity-drive",
        title: "Activity: iCloud Drive",
        subtitle: "View iCloud Drive synchronization history",
        category: "Navigation",
        icon: <CloudRegular />,
        keywords: ["drive", "files", "documents", "icloud"],
        perform: () => {
          navigate("/activity", { search: { tab: "drive" } });
        },
      },
      {
        id: "nav-activity-iphone",
        title: "Activity: iPhone backups",
        subtitle: "View local iPhone backup checks",
        category: "Navigation",
        icon: <HistoryRegular />,
        keywords: ["iphone", "ios", "backup", "copies"],
        perform: () => {
          navigate("/activity", { search: { tab: "iphone" } });
        },
      },
      {
        id: "nav-activity-events",
        title: "Activity: Events",
        subtitle: "Search append-only event log",
        category: "Navigation",
        icon: <HistoryRegular />,
        keywords: ["events", "log", "audit"],
        perform: () => {
          navigate("/activity", { search: { tab: "events" } });
        },
      },
      {
        id: "nav-quarantine",
        title: "Quarantine",
        subtitle: "Inspect failing files and review bit-rot",
        category: "Navigation",
        icon: <ShieldErrorRegular />,
        keywords: ["corrupt", "failed", "quarantine", "damage"],
        perform: () => {
          navigate("/quarantine");
        },
      },
      {
        id: "nav-settings",
        title: "Settings",
        subtitle: "Configure archive path, sync intervals, and schedule",
        category: "Navigation",
        icon: <SettingsRegular />,
        keywords: ["preferences", "config", "schedule", "interval"],
        perform: () => {
          navigate("/settings");
        },
      },
      {
        id: "action-start-sync",
        title: "Start Sync Now",
        subtitle: "Trigger immediate iCloud sync cycle",
        category: "Actions",
        icon: <PlayRegular />,
        keywords: ["sync", "run", "download", "fetch"],
        perform: () => {
          void startSync.mutateAsync({});
        },
      },
      {
        id: "action-toggle-theme",
        title: preference === "dark" ? "Switch to Light Theme" : "Switch to Dark Theme",
        subtitle: `Currently using ${preference} theme`,
        category: "Actions",
        icon: preference === "dark" ? <WeatherSunnyRegular /> : <WeatherMoonRegular />,
        keywords: ["theme", "dark", "light", "mode", "color"],
        perform: () => {
          setPreference(preference === "dark" ? "light" : "dark");
        },
      },
    ];
  }, [preference, setPreference, startSync]);

  const filteredCommands = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (q === "") {
      return commands;
    }
    return commands.filter((cmd) => {
      const matchTitle = cmd.title.toLowerCase().includes(q);
      const matchSubtitle = cmd.subtitle?.toLowerCase().includes(q) ?? false;
      const matchCategory = cmd.category.toLowerCase().includes(q);
      const matchKeywords = cmd.keywords?.some((k) => k.toLowerCase().includes(q)) ?? false;
      return matchTitle || matchSubtitle || matchCategory || matchKeywords;
    });
  }, [commands, query]);

  useEffect(() => {
    if (open) {
      const timer = setTimeout(() => {
        inputRef.current?.focus();
      }, 50);
      return () => {
        clearTimeout(timer);
      };
    }
  }, [open]);

  const executeItem = (cmd: Command) => {
    onOpenChange(false);
    cmd.perform();
  };

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setActiveIndex((prev) => (prev < filteredCommands.length - 1 ? prev + 1 : 0));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setActiveIndex((prev) => (prev > 0 ? prev - 1 : filteredCommands.length - 1));
    } else if (e.key === "Enter") {
      e.preventDefault();
      const selected = filteredCommands[activeIndex];
      if (selected !== undefined) {
        executeItem(selected);
      }
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(_, data) => {
        onOpenChange(data.open);
      }}
    >
      <DialogSurface className={styles.surface}>
        <DialogBody className={styles.body}>
          <SearchBox
            ref={inputRef}
            placeholder="Type a page, command or action…"
            value={query}
            onChange={(_, d) => {
              setQuery(d.value);
              setActiveIndex(0);
            }}
            onKeyDown={handleKeyDown}
          />
          <div className={styles.list} role="listbox" aria-label="Command palette suggestions">
            {filteredCommands.length === 0 ? (
              <div className={styles.empty}>No commands match &ldquo;{query}&rdquo;</div>
            ) : (
              filteredCommands.map((cmd, idx) => (
                <button
                  type="button"
                  key={cmd.id}
                  className={mergeClasses(styles.item, idx === activeIndex && styles.itemActive)}
                  role="option"
                  aria-selected={idx === activeIndex}
                  onClick={() => {
                    executeItem(cmd);
                  }}
                  onMouseEnter={() => {
                    setActiveIndex(idx);
                  }}
                >
                  <span className={styles.icon}>{cmd.icon}</span>
                  <div className={styles.textContainer}>
                    <Text className={styles.title}>{cmd.title}</Text>
                    {cmd.subtitle !== undefined && <Text className={styles.subtitle}>{cmd.subtitle}</Text>}
                  </div>
                </button>
              ))
            )}
          </div>
          <div className={styles.footer}>
            <span>
              <span className={styles.kbd}>↑↓</span> Navigate · <span className={styles.kbd}>Enter</span> Select
            </span>
            <span>
              <span className={styles.kbd}>Esc</span> Close
            </span>
          </div>
        </DialogBody>
      </DialogSurface>
    </Dialog>
  );
}
