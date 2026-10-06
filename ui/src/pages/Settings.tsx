import { useMutation, useQuery } from "@connectrpc/connect-query";
import {
  Badge,
  Button,
  Caption1,
  Card,
  makeStyles,
  Spinner,
  Subtitle2,
  Text,
  ToggleButton,
  tokens,
} from "@fluentui/react-components";
import { CopyRegular } from "@fluentui/react-icons";
import { useState, type ReactNode } from "react";

import { useThemePreference, type ThemePreference } from "../app/themeContext";
import { Page } from "../components/Page";
import { useNotify } from "../components/toast";
import { formatInterval } from "../format";
import { AgentService } from "../gen/homecloud/agent/v1/agent_pb";

const useStyles = makeStyles({
  intro: { color: tokens.colorNeutralForeground3, maxWidth: "760px" },
  group: { padding: 0, gap: 0 },
  row: {
    display: "flex",
    flexWrap: "wrap",
    alignItems: "center",
    gap: `${tokens.spacingVerticalS} ${tokens.spacingHorizontalL}`,
    padding: `${tokens.spacingVerticalM} ${tokens.spacingHorizontalXL}`,
    minHeight: "44px",
    borderBottom: `1px solid ${tokens.colorNeutralStroke2}`,
    ":last-child": { borderBottom: "none" },
  },
  label: { flex: "1 1 260px", minWidth: 0, display: "flex", flexDirection: "column" },
  mono: { fontFamily: tokens.fontFamilyMonospace, overflowWrap: "anywhere", color: tokens.colorNeutralForeground3 },
  buttons: { display: "flex", flexWrap: "wrap", gap: tokens.spacingHorizontalS },
});

function Row({
  label,
  detail,
  mono,
  children,
}: {
  label: string;
  detail?: string;
  mono?: boolean;
  children?: ReactNode;
}) {
  const styles = useStyles();
  return (
    <div className={styles.row}>
      <div className={styles.label}>
        <Text>{label}</Text>
        {detail !== undefined && <Caption1 className={mono === true ? styles.mono : undefined}>{detail}</Caption1>}
      </div>
      {children}
    </div>
  );
}

function Group({ title, children }: { title: string; children: ReactNode }) {
  const styles = useStyles();
  return (
    <section>
      <Subtitle2 as="h2" block>
        {title}
      </Subtitle2>
      <Card className={styles.group}>{children}</Card>
    </section>
  );
}

const themes: { value: ThemePreference; label: string }[] = [
  { value: "system", label: "System" },
  { value: "light", label: "Light" },
  { value: "dark", label: "Dark" },
];

export function SettingsPage() {
  const styles = useStyles();
  const notify = useNotify();
  const settings = useQuery(AgentService.method.getSettings, {});
  const info = useQuery(AgentService.method.getInfo, {});
  const { preference, setPreference } = useThemePreference();
  const [showTopic, setShowTopic] = useState(false);
  const test = useMutation(AgentService.method.sendTestNotification, {
    onSuccess: () => {
      notify(
        "success",
        "Test notification sent",
        "If it doesn't arrive, check that the ntfy app is subscribed to this topic.",
      );
    },
    onError: (err) => {
      notify("error", "Couldn't send the test notification", err.message);
    },
  });

  const s = settings.data?.settings;
  if (s === undefined) {
    return (
      <Page title="Settings">
        <Spinner label="Loading settings…" />
      </Page>
    );
  }
  const copy = (text: string, what: string) => {
    navigator.clipboard.writeText(text).then(
      () => {
        notify("success", `${what} copied`);
      },
      () => {
        notify("error", `Couldn't copy the ${what.toLowerCase()}`);
      },
    );
  };

  return (
    <Page title="Settings">
      <Text className={styles.intro}>
        Archive and schedule settings live in <code>{s.configPath}</code> and are checked when the agent starts. A bad
        value stops the agent with a clear error instead of being guessed around.
      </Text>

      <Group title="Archive">
        <Row label="Location" detail={s.storageRoot} mono />
        <Row label="Drive identity" detail={s.storageVolumeId} mono />
        <Row label="Keep free on the drive" detail="Downloads stop below this">
          <Text>{s.minFreePercent}%</Text>
        </Row>
      </Group>

      <Group title="iCloud">
        <Row label="Apple Account" detail={s.appleId} />
        <Row
          label="Hidden photos"
          detail={
            s.includeHidden
              ? "Archived in Photos/Hidden, a folder hidden on disk"
              : "Not archived; they stay only in iCloud (icloud.include_hidden)"
          }
        >
          <Badge appearance="tint" color={s.includeHidden ? "success" : "informative"}>
            {s.includeHidden ? "On" : "Off"}
          </Badge>
        </Row>
        <Row label="Downloader" detail={`kei ${s.downloader?.version ?? ""} · ${s.downloader?.binary ?? ""}`} mono />
        <Row
          label="Downloader configuration"
          detail={`${s.downloaderConfig} (written by homecloud from the settings above)`}
          mono
        />
        <Row label="Check for new items" detail="Downloads new photos and videos, then verifies them">
          <Text>{formatInterval(s.syncIntervalSeconds)}</Text>
        </Row>
      </Group>

      <Group title="Verification">
        <Row
          label="Video checker"
          detail={`ffprobe ${s.videoProbe?.version ?? ""} · ${s.videoProbe?.binary ?? ""}`}
          mono
        />
        <Row
          label="Re-check archived files"
          detail={`${Math.round(s.scrubFraction * 100)}% of files per run, those checked longest ago first (schedule.scrub_interval, schedule.scrub_fraction)`}
        >
          <Text>{formatInterval(s.scrubIntervalSeconds)}</Text>
        </Row>
        <Row
          label="Records backup"
          detail={`${s.backupDir} · newest ${s.backupKeep} kept (backup.dir must be on another drive)`}
          mono
        >
          <Text>{formatInterval(s.backupIntervalSeconds)}</Text>
        </Row>
      </Group>

      <Group title="Notifications">
        <Row label="ntfy topic" detail={`${s.notifyServer}/${showTopic ? s.notifyTopic : "•".repeat(16)}`} mono>
          <div className={styles.buttons}>
            <Button
              onClick={() => {
                setShowTopic((v) => !v);
              }}
            >
              {showTopic ? "Hide" : "Show"}
            </Button>
            <Button
              icon={<CopyRegular />}
              onClick={() => {
                copy(s.notifyTopic, "Topic");
              }}
            >
              Copy
            </Button>
          </div>
        </Row>
        <Row label="Test" detail="Alerts never include file names; the topic is public to anyone who knows it.">
          <Button
            disabled={test.isPending}
            onClick={() => {
              test.mutate({});
            }}
          >
            Send test notification
          </Button>
        </Row>
      </Group>

      <Group title="This app">
        <Row label="Appearance">
          <div className={styles.buttons} role="group" aria-label="Appearance">
            {themes.map((t) => (
              <ToggleButton
                key={t.value}
                checked={preference === t.value}
                onClick={() => {
                  setPreference(t.value);
                }}
              >
                {t.label}
              </ToggleButton>
            ))}
          </div>
        </Row>
        <Row
          label="About"
          detail={
            info.data === undefined
              ? undefined
              : `Agent ${info.data.agentVersion} on ${info.data.os} · API ${info.data.apiVersion} · listening on ${s.listen}`
          }
        />
      </Group>
    </Page>
  );
}
