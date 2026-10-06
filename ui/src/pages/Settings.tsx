import { useMutation, useQuery } from "@connectrpc/connect-query";
import {
  Badge,
  Button,
  Caption1,
  Card,
  Dialog,
  DialogActions,
  DialogBody,
  DialogContent,
  DialogSurface,
  DialogTitle,
  DialogTrigger,
  makeStyles,
  MessageBar,
  MessageBarBody,
  Spinner,
  Subtitle2,
  Text,
  ToggleButton,
  tokens,
} from "@fluentui/react-components";
import { CopyRegular } from "@fluentui/react-icons";
import { useState, type ReactNode } from "react";

import { usePlatform } from "../app/platformContext";
import { useThemePreference, type ThemePreference } from "../app/themeContext";
import { LoadError } from "../components/LoadError";
import { PairForm } from "../components/PairForm";
import { Page } from "../components/Page";
import { useNotify } from "../components/toast";
import { useCopy } from "../components/useCopy";
import { formatInterval, formatSpan, formatWhen, toDate } from "../format";
import { icloudLoginCommand } from "../shell";
import { AgentService } from "../gen/homecloud/agent/v3/agent_pb";

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
  const copy = useCopy();
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

  if (settings.isError) {
    return (
      <Page title="Settings">
        <LoadError what="the settings" error={settings.error} onRetry={() => void settings.refetch()} />
      </Page>
    );
  }
  const s = settings.data?.settings;
  if (s === undefined) {
    return (
      <Page title="Settings">
        <Spinner label="Loading settings…" />
      </Page>
    );
  }

  return (
    <Page title="Settings">
      <Text className={styles.intro}>
        Archive and schedule settings live in <code>{s.configPath}</code> and are checked when the agent starts. A bad
        value stops the agent with a clear error instead of being guessed around.
      </Text>

      <Group title="Archive">
        <Row label="Location" detail={s.storageRoot} mono />
        <Row label="Archive id" detail={`${s.storageArchiveId} (in homecloud-archive.json at the archive root)`} mono />
        <Row label="Keep free on the drive" detail="New downloads pause below this; archived files are still checked">
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
          label="Thumbnails"
          detail={`ffmpeg ${s.thumbnailer?.version ?? ""} · ${s.thumbnailer?.binary ?? ""}`}
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

      <Group title="iCloud Drive">
        <Row label="Archive folder" detail={`${s.driveDir} (never deleted from; replaced copies in .versions)`} mono />
        <Row
          label="Archive iCloud Drive"
          detail={`${s.driveWorkers} files at a time; every file is checked against Apple's checksum (drive.sync_interval, drive.workers)`}
        >
          <Text>{formatInterval(s.driveSyncIntervalSeconds)}</Text>
        </Row>
        <Row
          label="Sign-in"
          detail={
            info.data === undefined
              ? "homecloud's own iCloud session"
              : `homecloud's own iCloud session; renew it with: ${icloudLoginCommand(info.data.os, s)}`
          }
          mono
        />
      </Group>

      <Group title="iPhone backups">
        <Row
          label="Watched folders"
          detail={`${s.iphoneBackupDirs.join(" · ")} (read only; Apple's Devices app writes the backups)`}
          mono
        />
        <Row
          label="Check backups"
          detail={`Alerts when the newest backup is older than ${formatSpan(s.iphoneBackupMaxAgeSeconds)}, unencrypted, unfinished, unreadable, or ${s.iphoneBackupMaxShrinkPercent}% smaller than the last healthy one`}
        >
          <Text>{formatInterval(s.iphoneBackupCheckIntervalSeconds)}</Text>
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

      <ConnectionGroup />
      <PairedDevicesGroup />

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

/** Which homecloud the desktop app uses, and switching it. */
function ConnectionGroup() {
  const styles = useStyles();
  const platform = usePlatform();
  const [error, setError] = useState<string>();
  if (platform.kind !== "desktop") {
    return null;
  }
  const c = platform.connection;
  return (
    <Group title="Connection">
      {c.kind === "this_pc" ? (
        <Row label="Uses" detail="The homecloud agent on this computer">
          <Dialog>
            <DialogTrigger disableButtonEnhancement>
              <Button>Connect to another device…</Button>
            </DialogTrigger>
            <DialogSurface>
              <DialogBody>
                <DialogTitle>Connect to homecloud on another device</DialogTitle>
                <DialogContent>
                  <Text block>
                    For homecloud running on a NAS, reached through Tailscale. On that device run{" "}
                    <code>homecloud open --print</code> (in Docker:{" "}
                    <code>docker compose exec homecloud homecloud open --print</code>) for a one-time code. This
                    computer then keeps its own credential in the system's credential store.
                  </Text>
                  <PairForm initialUrl="" deviceName={platform.deviceName} pair={platform.pair} />
                </DialogContent>
                <DialogActions>
                  <DialogTrigger disableButtonEnhancement>
                    <Button>Cancel</Button>
                  </DialogTrigger>
                </DialogActions>
              </DialogBody>
            </DialogSurface>
          </Dialog>
        </Row>
      ) : (
        <Row label="Uses" detail={c.url} mono>
          <div className={styles.buttons}>
            <Button
              onClick={() => {
                setError(undefined);
                platform.disconnect().catch((err: unknown) => {
                  setError(String(err));
                });
              }}
            >
              Disconnect
            </Button>
            {error !== undefined && (
              <Button
                onClick={() => {
                  void platform.forget();
                }}
              >
                Forget anyway
              </Button>
            )}
          </div>
        </Row>
      )}
      {error !== undefined && (
        <MessageBar intent="warning">
          <MessageBarBody>
            Couldn't revoke this computer there ({error}). Forget it anyway, then revoke it there under Paired devices.
          </MessageBarBody>
        </MessageBar>
      )}
    </Group>
  );
}

/** Devices paired with this homecloud, each revocable. */
function PairedDevicesGroup() {
  const platform = usePlatform();
  const notify = useNotify();
  const devices = useQuery(AgentService.method.listDevices, {});
  const revoke = useMutation(AgentService.method.revokeDevice, {
    onSuccess: () => {
      void devices.refetch();
    },
    onError: (err) => {
      notify("error", "Couldn't revoke the device", err.message);
    },
  });
  const self = platform.kind === "desktop" && platform.connection.kind === "remote" ? platform.connection.deviceId : "";
  return (
    <Group title="Paired devices">
      {devices.isPending && <Spinner size="tiny" label="Loading paired devices…" />}
      {devices.isError && (
        <LoadError what="the paired devices" error={devices.error} onRetry={() => void devices.refetch()} />
      )}
      {devices.data?.devices.length === 0 && (
        <Row label="None" detail="The desktop app on another computer appears here once it is paired." />
      )}
      {devices.data?.devices.map((d) => {
        const paired = toDate(d.pairedAt);
        const seen = toDate(d.lastSeenAt);
        return (
          <Row
            key={d.id}
            label={d.id === self ? `${d.name} (this computer)` : d.name}
            detail={[
              paired === undefined ? "" : `Paired ${formatWhen(paired)}`,
              seen === undefined ? "" : `last seen ${formatWhen(seen)}`,
            ]
              .filter((x) => x !== "")
              .join(" · ")}
          >
            <Dialog>
              <DialogTrigger disableButtonEnhancement>
                <Button aria-label={`Revoke ${d.name}`}>Revoke…</Button>
              </DialogTrigger>
              <DialogSurface>
                <DialogBody>
                  <DialogTitle>Revoke {d.name}?</DialogTitle>
                  <DialogContent>
                    It can no longer use this homecloud. Pairing it again needs a new one-time code.
                  </DialogContent>
                  <DialogActions>
                    <DialogTrigger disableButtonEnhancement>
                      <Button>Cancel</Button>
                    </DialogTrigger>
                    <Button
                      appearance="primary"
                      disabled={revoke.isPending}
                      onClick={() => {
                        if (d.id === self && platform.kind === "desktop") {
                          void platform.forget();
                          return;
                        }
                        revoke.mutate({ deviceId: d.id });
                      }}
                    >
                      Revoke
                    </Button>
                  </DialogActions>
                </DialogBody>
              </DialogSurface>
            </Dialog>
          </Row>
        );
      })}
    </Group>
  );
}
