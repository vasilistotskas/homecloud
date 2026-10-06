import type { Timestamp } from "@bufbuild/protobuf/wkt";
import { useMutation, useQuery } from "@connectrpc/connect-query";
import {
  Badge,
  Button,
  Caption1,
  Card,
  CardHeader,
  Link,
  makeStyles,
  MessageBar,
  MessageBarBody,
  MessageBarTitle,
  Spinner,
  Subtitle2,
  Table,
  TableBody,
  TableCell,
  TableHeader,
  TableHeaderCell,
  TableRow,
  Text,
  Title3,
  tokens,
} from "@fluentui/react-components";
import {
  ArrowSyncRegular,
  CheckmarkCircleRegular,
  ClockRegular,
  CloudArchiveRegular,
  HardDriveRegular,
  HeartPulseRegular,
  KeyRegular,
  PhoneRegular,
  PlugDisconnectedRegular,
  ShieldCheckmarkRegular,
  ShieldErrorRegular,
} from "@fluentui/react-icons";
import { Link as RouterLink, useNavigate } from "@tanstack/react-router";
import { Fragment, useEffect, useState, type ReactNode } from "react";

import { useOverview } from "../api/overviewContext";
import { usePlatform } from "../app/platformContext";
import { CommandDialog, type CommandState } from "../components/CommandDialog";
import { LoadError } from "../components/LoadError";
import {
  driveHealthBadge,
  driveSyncListed,
  driveSyncOutcomeBadge,
  iphoneSeverity,
  iphoneStateBadge,
  type BadgeColor,
  type Labelled,
} from "../components/labels";
import { Page } from "../components/Page";
import { runRow } from "../components/runs";
import { useNotify } from "../components/toast";
import { formatBytes, formatCount, formatInterval, formatRelative, formatWhen, toDate } from "../format";
import { icloudLoginCommand, keiLoginCommand, terminalName } from "../shell";
import {
  AgentService,
  AppleAccountState,
  DriveHealthState,
  DownloadOutcome,
  DriveSyncOutcome,
  Phase,
  RunKind,
  RunResult,
  type DriveHealth,
  type GetInfoResponse,
  type Run,
  type Overview,
  type Settings,
} from "../gen/homecloud/agent/v2/agent_pb";

const useStyles = makeStyles({
  hero: {
    display: "flex",
    flexDirection: "row",
    flexWrap: "wrap",
    alignItems: "center",
    gap: tokens.spacingHorizontalL,
  },
  heroIcon: {
    width: "48px",
    height: "48px",
    borderRadius: tokens.borderRadiusCircular,
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    flex: "none",
    fontSize: "28px",
  },
  heroText: { flex: "1 1 320px", minWidth: 0, display: "flex", flexDirection: "column", gap: tokens.spacingVerticalXS },
  stats: {
    display: "grid",
    gridTemplateColumns: "repeat(auto-fit, minmax(min(190px, 100%), 1fr))",
    gap: tokens.spacingHorizontalM,
  },
  stat: { display: "flex", flexDirection: "column", gap: tokens.spacingVerticalXXS },
  cards: {
    display: "grid",
    gridTemplateColumns: "repeat(auto-fit, minmax(min(340px, 100%), 1fr))",
    gap: tokens.spacingHorizontalM,
  },
  facts: {
    display: "grid",
    gridTemplateColumns: "auto 1fr",
    gap: `${tokens.spacingVerticalXS} ${tokens.spacingHorizontalL}`,
  },
  muted: { color: tokens.colorNeutralForeground3 },
  bar: {
    position: "relative",
    height: "12px",
    borderRadius: tokens.borderRadiusMedium,
    overflow: "hidden",
    display: "flex",
    backgroundColor: tokens.colorNeutralBackground5,
  },
  barArchive: { backgroundColor: tokens.colorBrandBackground },
  barOther: { backgroundColor: tokens.colorNeutralStroke1 },
  marker: { position: "absolute", top: 0, bottom: 0, width: "2px", backgroundColor: tokens.colorNeutralForeground1 },
  legend: { display: "flex", flexWrap: "wrap", gap: tokens.spacingHorizontalL },
  legendItem: { display: "flex", alignItems: "center", gap: tokens.spacingHorizontalXS },
  swatch: { width: "10px", height: "10px", borderRadius: tokens.borderRadiusSmall },
  offline: {
    alignItems: "center",
    textAlign: "center",
    padding: `${tokens.spacingVerticalXXXL} ${tokens.spacingHorizontalXXL}`,
    gap: tokens.spacingVerticalL,
  },
  tableBox: { overflowX: "auto" },
  nowrap: { whiteSpace: "nowrap" },
  // Fluent tables default to equal fixed-width columns; size columns to their content instead.
  table: { tableLayout: "auto" },
  offlineActions: { display: "flex", flexWrap: "wrap", justifyContent: "center", gap: tokens.spacingHorizontalM },
});

export function OverviewPage() {
  const styles = useStyles();
  const { overview, connection, receivedAt, reconnect, problem } = useOverview();
  const settings = useQuery(AgentService.method.getSettings, {});
  const info = useQuery(AgentService.method.getInfo, {});
  const notify = useNotify();
  const phase = overview?.activity?.phase ?? Phase.UNSPECIFIED;
  const syncing = cyclePhases.has(phase);
  const startSync = useMutation(AgentService.method.startSync, {
    onSuccess: (res) => {
      if (!res.started) {
        notify("info", "A sync is already running or queued");
      } else if (phase !== Phase.IDLE) {
        notify("info", "Sync queued: it starts as soon as the current job finishes");
      }
    },
    onError: (err) => {
      notify("error", "Couldn't start a sync", err.message);
    },
  });

  const actions = (
    <Button
      appearance="primary"
      icon={<ArrowSyncRegular />}
      disabled={connection !== "live" || syncing || startSync.isPending}
      onClick={() => {
        startSync.mutate({});
      }}
    >
      {syncing ? "Syncing…" : "Sync now"}
    </Button>
  );

  if (connection === "archive-unavailable") {
    return (
      <Page title="Overview" actions={actions}>
        <ArchiveUnavailable problem={problem} />
      </Page>
    );
  }
  if (connection === "reconnecting" || (connection === "connecting" && overview === undefined)) {
    return (
      <Page title="Overview" actions={actions}>
        {connection === "connecting" ? (
          <Spinner label="Connecting to the agent…" />
        ) : (
          <Offline lastContact={receivedAt} onRetry={reconnect} />
        )}
      </Page>
    );
  }
  if (overview === undefined) {
    return <Page title="Overview">{null}</Page>;
  }
  const env: Env = {
    settings: settings.data?.settings,
    commands: commandsFrom(settings, info),
  };
  return (
    <Page title="Overview" actions={actions}>
      <PhaseBanner overview={overview} />
      <Hero overview={overview} env={env} />
      <Stats overview={overview} />
      <div className={styles.cards}>
        <StorageCard overview={overview} />
        <AccountCard overview={overview} />
        <ScheduleCard overview={overview} settings={env.settings} />
        <DriveCard drive={overview.drive} />
        <IntegrityCard overview={overview} settings={env.settings} />
        <IPhoneBackupCard overview={overview} settings={env.settings} />
        <ICloudDriveCard overview={overview} env={env} />
      </div>
      <RecentSyncs lastCycleId={overview.lastCycle?.id} />
    </Page>
  );
}

/** Phases of a sync cycle; the others are idle time or maintenance jobs. */
const cyclePhases: ReadonlySet<Phase> = new Set([
  Phase.DOWNLOADING,
  Phase.INGESTING,
  Phase.VERIFYING,
  Phase.PREPARING_PREVIEWS,
]);

function ArchiveUnavailable({ problem }: { problem: string | undefined }) {
  return (
    <MessageBar intent="error">
      <MessageBarBody>
        <MessageBarTitle>The archive drive isn&apos;t available</MessageBarTitle>
        The agent is running, but the drive that holds your archive and its records is missing or a different drive is
        mounted in its place. Nothing is downloaded, checked or deleted until it&apos;s back; this page reconnects by
        itself. {problem}
      </MessageBarBody>
    </MessageBar>
  );
}

function Offline({ lastContact, onRetry }: { lastContact: Date | undefined; onRetry: () => void }) {
  const styles = useStyles();
  const platform = usePlatform();
  const notify = useNotify();
  const [starting, setStarting] = useState(false);
  return (
    <Card className={styles.offline}>
      <div
        className={styles.heroIcon}
        style={{ backgroundColor: tokens.colorPaletteRedBackground1, color: tokens.colorPaletteRedForeground1 }}
      >
        <PlugDisconnectedRegular />
      </div>
      <Title3 as="h2">Can&apos;t reach the homecloud agent</Title3>
      {platform.kind === "desktop" && platform.connection.kind === "remote" ? (
        <Text className={styles.muted}>
          homecloud at {platform.connection.url} isn&apos;t answering. Check that Tailscale is connected on this
          computer and that the device running homecloud is on. Your files there are not affected.
        </Text>
      ) : (
        <Text className={styles.muted}>
          The agent downloads and checks your files in the background and starts when you sign in. Your files on disk
          are not affected. It may have stopped, or still be starting.
        </Text>
      )}
      <div className={styles.offlineActions}>
        {platform.kind === "desktop" && platform.connection.kind === "this_pc" && (
          <Button
            appearance="primary"
            disabled={starting}
            onClick={() => {
              setStarting(true);
              platform.startAgent().then(
                () => {
                  setStarting(false);
                  onRetry();
                },
                (err: unknown) => {
                  setStarting(false);
                  notify("error", "Couldn't start the agent", String(err));
                },
              );
            }}
          >
            {starting ? "Starting…" : "Start agent"}
          </Button>
        )}
        <Button onClick={onRetry}>Try again</Button>
      </div>
      {lastContact !== undefined && (
        <Caption1 className={styles.muted}>Last contact: {formatWhen(lastContact)}</Caption1>
      )}
    </Card>
  );
}

const phaseText: Record<Phase, string> = {
  [Phase.UNSPECIFIED]: "",
  [Phase.IDLE]: "",
  [Phase.DOWNLOADING]: "Downloading new items from iCloud…",
  [Phase.INGESTING]: "Recording what was downloaded…",
  [Phase.VERIFYING]: "Checking files…",
  [Phase.PREPARING_PREVIEWS]: "Preparing previews for the library…",
  [Phase.CHECKING_ARCHIVE]: "Re-checking archived files against their checksums…",
  [Phase.BACKING_UP_LEDGER]: "Backing up the archive's records…",
  [Phase.CHECKING_IPHONE_BACKUPS]: "Checking the iPhone backups on this computer…",
  [Phase.SYNCING_DRIVE]: "Archiving new and changed iCloud Drive files…",
};

function PhaseBanner({ overview }: { overview: Overview }) {
  const phase = overview.activity?.phase ?? Phase.UNSPECIFIED;
  if (phaseText[phase] === "") {
    return null;
  }
  const started = toDate(overview.activity?.phaseStartedAt);
  return (
    <MessageBar intent="info" icon={<Spinner size="tiny" />}>
      <MessageBarBody>
        {phaseText[phase]}
        {started !== undefined && ` Started ${formatRelative(started)}.`} You can close this window; the agent keeps
        going.
      </MessageBarBody>
    </MessageBar>
  );
}

/** What the cards and banner need besides the overview. */
interface Env {
  settings: Settings | undefined;
  commands: { os: string; settings: Settings } | CommandState;
}

/** The parts of a query the command builder reads. */
interface Query<T> {
  data?: T;
  isError: boolean;
  error: unknown;
  refetch: () => unknown;
}

/** The settings and OS the shown commands are built from, or why they are not available. */
function commandsFrom(settings: Query<{ settings?: Settings }>, info: Query<GetInfoResponse>): Env["commands"] {
  if (settings.isError || info.isError) {
    return {
      kind: "error",
      error: settings.isError ? settings.error : info.error,
      retry: () => {
        void settings.refetch();
        void info.refetch();
      },
    };
  }
  const s = settings.data?.settings;
  const os = info.data?.os;
  return s === undefined || os === undefined ? { kind: "loading" } : { os, settings: s };
}

/** A command for the dialogs, built when its inputs are there. */
function command(env: Env, build: (os: string, s: Settings) => string): CommandState {
  return "os" in env.commands
    ? { kind: "ready", command: build(env.commands.os, env.commands.settings) }
    : env.commands;
}

/** "In PowerShell" or "in a terminal", for the agent's OS. */
function terminal(env: Env): string {
  return "os" in env.commands ? terminalName(env.commands.os) : "a terminal";
}

/** A detail sentence: the detail, when there is one, then the rest. */
function sentence(detail: string, rest: string): string {
  return detail === "" ? rest : `${detail.replace(/[.\s]+$/, "")}. ${rest}`;
}

type Tone = "success" | "warning" | "danger" | "brand";

/** One condition the banner can show; the most severe present wins. */
interface Problem {
  rank: number;
  tone: Tone;
  icon: ReactNode;
  title: string;
  body: string;
  action?: ReactNode;
}

function Hero({ overview, env }: { overview: Overview; env: Env }) {
  const styles = useStyles();
  const a = overview.assets;
  const quarantined = a?.quarantined ?? 0n;
  const pending = (a?.discovered ?? 0n) + (a?.downloaded ?? 0n);
  const account = overview.appleAccount?.state ?? AppleAccountState.UNSPECIFIED;
  const drive = overview.drive?.state ?? DriveHealthState.UNSPECIFIED;
  const cycle = overview.lastCycle?.cycle;
  const scrub = overview.lastScrub;
  const backup = overview.lastLedgerBackup;
  const iphone = iphoneBackupSummary(overview.lastIphoneBackup, env.settings);
  const driveSync = overview.lastDriveSync?.driveSync;
  const minFree = overview.storage?.minFreePercent ?? 0;
  const damaged = scrub?.scrub?.drive?.damaged ?? 0n;

  // Danger outranks every warning; within each, the order is how much the
  // problem threatens the archive.
  const problems: Problem[] = [];
  if (drive === DriveHealthState.FAILING || drive === DriveHealthState.WARNING) {
    problems.push({
      rank: 100,
      tone: "danger",
      icon: <HardDriveRegular />,
      title:
        drive === DriveHealthState.FAILING
          ? "Your archive drive expects to fail"
          : "Your archive drive reports a problem",
      body: sentence(
        overview.drive?.reasons.join("; ") ?? "",
        "It holds your only copy: replace it soon, and keep iCloud cleanup off until then.",
      ),
    });
  }
  if (quarantined > 0n) {
    problems.push({
      rank: 95,
      tone: "danger",
      icon: <ShieldErrorRegular />,
      title: `${formatCount(quarantined)} ${quarantined === 1n ? "item" : "items"} failed verification`,
      body: "They're kept aside and will never be deleted from iCloud until you've reviewed them.",
      action: <ReviewButton />,
    });
  }
  if (damaged > 0n) {
    problems.push({
      rank: 90,
      tone: "danger",
      icon: <ShieldErrorRegular />,
      title: `${formatCount(damaged)} archived iCloud Drive ${damaged === 1n ? "file" : "files"} changed on disk`,
      body: "They are downloaded again at the next iCloud Drive sync and the damaged copies are kept aside. Check the archive drive's health.",
    });
  }
  if (iphone.tone === "danger") {
    problems.push({ rank: 85, tone: "danger", icon: <PhoneRegular />, title: iphone.title, body: iphone.detail });
  }
  if (account === AppleAccountState.SIGN_IN_REQUIRED || account === AppleAccountState.NEEDS_ATTENTION) {
    problems.push({
      rank: 70,
      tone: "warning",
      icon: <KeyRegular />,
      title:
        account === AppleAccountState.SIGN_IN_REQUIRED ? "Apple sign-in needed" : "Your Apple account needs attention",
      body: "New photos aren't being downloaded until you sign in again. Everything already archived is safe, and nothing is deleted from iCloud meanwhile.",
      action: <SignInDialog env={env} />,
    });
  }
  if (driveSync?.outcome === DriveSyncOutcome.SIGN_IN_REQUIRED) {
    problems.push({
      rank: 65,
      tone: "warning",
      icon: <KeyRegular />,
      title: "iCloud Drive sign-in needed",
      body: "homecloud's own iCloud session expired, so new and changed iCloud Drive files aren't archived until you sign in again. Everything already archived is kept.",
      action: <DriveSignInDialog env={env} />,
    });
  }
  if (
    cycle?.download === DownloadOutcome.SKIPPED_LOW_SPACE ||
    driveSync?.outcome === DriveSyncOutcome.SKIPPED_LOW_SPACE
  ) {
    problems.push({
      rank: 60,
      tone: "warning",
      icon: <HardDriveRegular />,
      title: "Your archive drive is almost full",
      body: `Nothing new is downloaded from iCloud Photos or iCloud Drive while less than ${minFree}% of the drive is free. Everything already archived is still checked.`,
    });
  }
  if (cycle?.download === DownloadOutcome.ERROR) {
    problems.push({
      rank: 55,
      tone: "warning",
      icon: <ArrowSyncRegular />,
      title: "The downloader couldn't run",
      body: "New photos weren't downloaded in the last sync. Activity and the agent's log say why; it is retried at the next sync.",
    });
  }
  if (cycle !== undefined && cycle.errored > 0n) {
    problems.push({
      rank: 50,
      tone: "warning",
      icon: <ShieldErrorRegular />,
      title: `${formatCount(cycle.errored)} ${cycle.errored === 1n ? "file" : "files"} couldn't be checked`,
      body: "They couldn't be read in the last sync and are checked again in the next one.",
    });
  }
  if (scrub?.result === RunResult.FAILED && (scrub.scrub?.error ?? "") !== "") {
    problems.push({
      rank: 45,
      tone: "warning",
      icon: <ShieldCheckmarkRegular />,
      title: "The integrity check couldn't run",
      body: sentence(scrub.scrub?.error ?? "", "It is retried at the next sync."),
    });
  }
  if (backup?.result === RunResult.FAILED) {
    problems.push({
      rank: 40,
      tone: "warning",
      icon: <ShieldCheckmarkRegular />,
      title: "The records backup failed",
      body: sentence(backup.ledgerBackup?.error ?? "", "It is retried at the next sync."),
    });
  }
  if (driveSync?.outcome === DriveSyncOutcome.PARTIAL || driveSync?.outcome === DriveSyncOutcome.ERROR) {
    problems.push({
      rank: 35,
      tone: "warning",
      icon: <CloudArchiveRegular />,
      title:
        driveSync.outcome === DriveSyncOutcome.ERROR
          ? "The iCloud Drive sync couldn't run"
          : `${formatCount(driveSync.failed)} iCloud Drive ${driveSync.failed === 1n ? "item" : "items"} couldn't be archived`,
      body: sentence(
        driveSync.error !== "" ? driveSync.error : (driveSync.failures[0] ?? ""),
        driveSync.outcome === DriveSyncOutcome.ERROR
          ? "It is retried at the next sync."
          : "They are retried at the next iCloud Drive sync.",
      ),
    });
  }
  if (iphone.tone === "warning") {
    problems.push({ rank: 30, tone: "warning", icon: <PhoneRegular />, title: iphone.title, body: iphone.detail });
  }
  if (pending > 0n) {
    problems.push({
      rank: 10,
      tone: "brand",
      icon: <ClockRegular />,
      title: `${formatCount(pending)} ${pending === 1n ? "item is" : "items are"} still being archived`,
      body: "They are downloaded and checked during the next sync.",
    });
  }
  const shown = problems.reduce<Problem>((worst, p) => (p.rank > worst.rank ? p : worst), {
    rank: 0,
    tone: "success",
    icon: <CheckmarkCircleRegular />,
    title: "Your archive is complete and verified",
    body: `Every item in iCloud Photos is in ${overview.storage?.root ?? "the archive"} and passed its checks.`,
  });

  const palette = {
    success: [tokens.colorPaletteGreenBackground1, tokens.colorPaletteGreenForeground1],
    warning: [tokens.colorPaletteYellowBackground1, tokens.colorPaletteDarkOrangeForeground1],
    danger: [tokens.colorPaletteRedBackground1, tokens.colorPaletteRedForeground1],
    brand: [tokens.colorBrandBackground2, tokens.colorBrandForeground1],
  }[shown.tone];

  return (
    <Card className={styles.hero}>
      <div className={styles.heroIcon} style={{ backgroundColor: palette[0], color: palette[1] }}>
        {shown.icon}
      </div>
      <div className={styles.heroText}>
        <Title3 as="h2">{shown.title}</Title3>
        <Text>{shown.body}</Text>
      </div>
      {shown.action}
    </Card>
  );
}

function SignInDialog({ env }: { env: Env }) {
  return (
    <CommandDialog
      trigger="Sign in…"
      triggerLabel="Sign in to iCloud Photos"
      title="Sign in to iCloud again"
      intro={`Run this in ${terminal(env)} on this computer. Your password and the code from your iPhone go straight to Apple; homecloud never sees them.`}
      command={command(env, keiLoginCommand)}
      after="Downloads resume at the next sync."
    />
  );
}

function DriveSignInDialog({ env }: { env: Env }) {
  return (
    <CommandDialog
      trigger="Sign in…"
      triggerLabel="Sign in to iCloud Drive"
      title="Sign homecloud in to iCloud Drive"
      intro={`Run this in ${terminal(env)} on this computer and type your Apple Account password, then the code from your iPhone. Your account uses Advanced Data Protection if it asks you to approve access on a device. The password is used once and never stored.`}
      command={command(env, icloudLoginCommand)}
      after="iCloud Drive is archived again at the next iCloud Drive sync; everything already archived stays."
    />
  );
}

function Stat({ label, value, note, danger }: { label: string; value: string; note: string; danger?: boolean }) {
  const styles = useStyles();
  return (
    <Card className={styles.stat}>
      <Caption1 className={styles.muted}>{label}</Caption1>
      <Title3 as="p" style={danger === true ? { color: tokens.colorPaletteRedForeground1 } : undefined}>
        {value}
      </Title3>
      <Caption1 className={styles.muted}>{note}</Caption1>
    </Card>
  );
}

function Stats({ overview }: { overview: Overview }) {
  const styles = useStyles();
  const a = overview.assets;
  const total = (a?.discovered ?? 0n) + (a?.downloaded ?? 0n) + (a?.verified ?? 0n) + (a?.quarantined ?? 0n);
  const quarantined = a?.quarantined ?? 0n;
  return (
    <div className={styles.stats}>
      <Stat label="Verified" value={formatCount(a?.verified ?? 0n)} note={`of ${formatCount(total)} items`} />
      <Stat
        label="Waiting for checks"
        value={formatCount((a?.discovered ?? 0n) + (a?.downloaded ?? 0n))}
        note="not yet downloaded or verified"
      />
      <Stat label="Quarantined" value={formatCount(quarantined)} note="need your review" danger={quarantined > 0n} />
      <Stat
        label="Archive size"
        value={formatBytes(overview.archive?.bytes ?? 0n)}
        note={`${formatCount(overview.archive?.files ?? 0n)} files`}
      />
    </div>
  );
}

function StorageCard({ overview }: { overview: Overview }) {
  const styles = useStyles();
  const s = overview.storage;
  const total = Number(s?.totalBytes ?? 0n);
  const free = Number(s?.freeBytes ?? 0n);
  const archive = Number(overview.archive?.bytes ?? 0n);
  const other = Math.max(0, total - free - archive);
  const pct = (v: number) => (total === 0 ? 0 : (v / total) * 100);
  const minFree = s?.minFreePercent ?? 0;
  return (
    <Card>
      <CardHeader
        image={<HardDriveRegular fontSize={20} />}
        header={<Subtitle2 as="h2">Storage</Subtitle2>}
        description={
          <Caption1 className={styles.muted}>
            {formatBytes(free)} free of {formatBytes(total)}
          </Caption1>
        }
      />
      <div
        className={styles.bar}
        role="img"
        aria-label={`Archive ${formatBytes(archive)}, other files ${formatBytes(other)}, free ${formatBytes(free)}`}
      >
        <div className={styles.barArchive} style={{ width: `${pct(archive)}%` }} />
        <div className={styles.barOther} style={{ width: `${pct(other)}%` }} />
        <div className={styles.marker} style={{ left: `${100 - minFree}%` }} />
      </div>
      <div className={styles.legend}>
        <Caption1 className={styles.legendItem}>
          <span className={`${styles.swatch} ${styles.barArchive}`} />
          homecloud {formatBytes(archive)}
        </Caption1>
        <Caption1 className={styles.legendItem}>
          <span className={`${styles.swatch} ${styles.barOther}`} />
          Other files {formatBytes(other)}
        </Caption1>
        <Caption1 className={styles.legendItem}>
          <span className={styles.swatch} style={{ width: "2px", backgroundColor: tokens.colorNeutralForeground1 }} />
          Keeps at least {minFree}% free
        </Caption1>
      </div>
    </Card>
  );
}

const accountBadge: Record<AppleAccountState, Labelled> = {
  [AppleAccountState.UNSPECIFIED]: { label: "Unknown", color: "informative" },
  [AppleAccountState.SIGNED_IN]: { label: "Signed in", color: "success" },
  [AppleAccountState.SIGN_IN_REQUIRED]: { label: "Sign-in needed", color: "warning" },
  [AppleAccountState.NEEDS_ATTENTION]: { label: "Needs attention", color: "danger" },
};

function AccountCard({ overview }: { overview: Overview }) {
  const styles = useStyles();
  const badge = accountBadge[overview.appleAccount?.state ?? AppleAccountState.UNSPECIFIED];
  const last = toDate(overview.lastCycle?.finishedAt);
  return (
    <Card>
      <CardHeader
        image={<KeyRegular fontSize={20} />}
        header={<Subtitle2 as="h2">Apple account</Subtitle2>}
        action={
          <Badge appearance="tint" color={badge.color}>
            {badge.label}
          </Badge>
        }
      />
      <div className={styles.facts}>
        <Text className={styles.muted}>Last sync</Text>
        <Text>{last === undefined ? "Not yet" : formatWhen(last)}</Text>
      </div>
    </Card>
  );
}

function ScheduleCard({ overview, settings }: { overview: Overview; settings: Settings | undefined }) {
  const styles = useStyles();
  const next = toDate(overview.activity?.nextCycleAt);
  return (
    <Card>
      <CardHeader image={<ClockRegular fontSize={20} />} header={<Subtitle2 as="h2">Schedule</Subtitle2>} />
      <div className={styles.facts}>
        <Text className={styles.muted}>Next sync</Text>
        <Text>
          {next === undefined ? "After the current one" : formatRelative(next)}
          {settings !== undefined && ` · ${formatInterval(settings.syncIntervalSeconds)}`}
        </Text>
      </div>
    </Card>
  );
}

function RecentSyncs({ lastCycleId }: { lastCycleId: bigint | undefined }) {
  const styles = useStyles();
  const runs = useQuery(AgentService.method.listRuns, { kind: RunKind.CYCLE, pageSize: 5 });
  const { refetch } = runs;
  useEffect(() => {
    void refetch();
  }, [lastCycleId, refetch]);
  return (
    <Card>
      <CardHeader
        header={<Subtitle2 as="h2">Recent syncs</Subtitle2>}
        action={
          <RouterLink to="/activity">
            <Link as="span">View all activity</Link>
          </RouterLink>
        }
      />
      <div className={styles.tableBox}>
        <Table size="small" aria-label="Recent syncs" className={styles.table}>
          <TableHeader>
            <TableRow>
              <TableHeaderCell>Started</TableHeaderCell>
              <TableHeaderCell>Download</TableHeaderCell>
              <TableHeaderCell>New</TableHeaderCell>
              <TableHeaderCell>Checked</TableHeaderCell>
              <TableHeaderCell>Took</TableHeaderCell>
              <TableHeaderCell>Result</TableHeaderCell>
            </TableRow>
          </TableHeader>
          <TableBody>
            {(runs.data?.runs ?? []).map(runRow).map((r) => (
              <TableRow key={r.id}>
                <TableCell className={styles.nowrap}>{r.started}</TableCell>
                <TableCell>{r.download}</TableCell>
                <TableCell>{r.added}</TableCell>
                <TableCell>{r.checked}</TableCell>
                <TableCell>{r.took}</TableCell>
                <TableCell>{r.result}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
      {runs.isError && <LoadError what="recent syncs" error={runs.error} onRetry={() => void runs.refetch()} />}
      {runs.data?.runs.length === 0 && <Text className={styles.muted}>No syncs yet.</Text>}
    </Card>
  );
}

function ReviewButton() {
  const navigate = useNavigate();
  return (
    <Button
      appearance="primary"
      onClick={() => {
        void navigate({ to: "/quarantine" });
      }}
    >
      Review
    </Button>
  );
}

function Facts({ facts }: { facts: [string, ReactNode][] }) {
  const styles = useStyles();
  return (
    <div className={styles.facts}>
      {facts.map(([k, v]) => (
        <Fragment key={k}>
          <Text className={styles.muted}>{k}</Text>
          <Text>{v}</Text>
        </Fragment>
      ))}
    </div>
  );
}

/** When a maintenance job runs next, as the agent reports it, for a fact line. */
function nextRun(at: Timestamp | undefined): string {
  const when = toDate(at);
  return when === undefined ? "After the current sync" : formatRelative(when);
}

function DriveCard({ drive }: { drive: DriveHealth | undefined }) {
  const styles = useStyles();
  const badge = driveHealthBadge[drive?.state ?? DriveHealthState.UNSPECIFIED];
  const checked = toDate(drive?.checkedAt);
  const facts: [string, string][] = [];
  if (drive !== undefined && drive.state !== DriveHealthState.UNAVAILABLE) {
    facts.push(["Disk", [drive.model, drive.protocol].filter((s) => s !== "").join(" · ")]);
    if (drive.temperatureCelsius !== undefined) facts.push(["Temperature", `${drive.temperatureCelsius} °C`]);
    if (drive.percentUsed !== undefined) facts.push(["Wear", `${drive.percentUsed}% of rated life`]);
    if (drive.mediaErrors !== undefined) facts.push(["Media errors", formatCount(drive.mediaErrors)]);
    if (drive.powerOnHours !== undefined) facts.push(["Powered on", `${formatCount(drive.powerOnHours)} hours`]);
  }
  return (
    <Card>
      <CardHeader
        image={<HeartPulseRegular fontSize={20} />}
        header={<Subtitle2 as="h2">Drive health</Subtitle2>}
        action={
          <Badge appearance="tint" color={badge.color}>
            {badge.label}
          </Badge>
        }
      />
      {facts.length > 0 && <Facts facts={facts} />}
      {drive?.state === DriveHealthState.UNAVAILABLE && (
        <Text className={styles.muted}>Health can&apos;t be read for this drive: {drive.reasons.join("; ")}</Text>
      )}
      <Caption1 className={styles.muted}>
        The archive drive holds your only copy. If it reports a problem you&apos;re alerted at once.
        {checked !== undefined && ` Checked ${formatWhen(checked)}.`}
      </Caption1>
    </Card>
  );
}

interface IPhoneSummary extends Labelled {
  /** The banner's tone: none when nothing needs the owner. */
  tone: "danger" | "warning" | "none";
  title: string;
  detail: string;
}

/** The overall verdict on the local iPhone backups, from the latest check. */
function iphoneBackupSummary(run: Run | undefined, settings: Settings | undefined): IPhoneSummary {
  const rep = run?.iphoneBackup;
  if (run === undefined || rep === undefined) {
    return { tone: "none", label: "Not checked yet", color: "informative", title: "", detail: "" };
  }
  if (rep.error !== "" || rep.dirErrors.length > 0) {
    return {
      tone: "warning",
      label: "Couldn't check",
      color: "warning",
      title: "The iPhone backups couldn't be checked",
      detail: sentence(
        [rep.error, ...rep.dirErrors].filter((e) => e !== "").join("; "),
        "It is retried at the next sync.",
      ),
    };
  }
  if (rep.devices.length === 0) {
    const every = settings === undefined ? "regularly" : formatInterval(settings.iphoneBackupCheckIntervalSeconds);
    return {
      tone: "warning",
      label: "No backup yet",
      color: "warning",
      title: "No iPhone backup on this computer yet",
      detail: `In Apple's Devices app, turn on "Encrypt local backup" and back up the iPhone once; homecloud then checks it ${every}.`,
    };
  }
  const worst = rep.devices.reduce((w, d) => (iphoneSeverity[d.state] > iphoneSeverity[w.state] ? d : w));
  const b = iphoneStateBadge[worst.state];
  const problems = rep.devices.flatMap((d) => d.reasons.map((r) => (rep.devices.length > 1 ? `${d.name}: ${r}` : r)));
  return {
    ...b,
    tone: b.color === "danger" ? "danger" : b.color === "warning" ? "warning" : "none",
    title: "An iPhone backup needs attention",
    detail: sentence(problems.join("; "), "Check it in Apple's Devices app."),
  };
}

function IPhoneBackupCard({ overview, settings }: { overview: Overview; settings: Settings | undefined }) {
  const styles = useStyles();
  const run = overview.lastIphoneBackup;
  const summary = iphoneBackupSummary(run, settings);
  const checked = toDate(run?.finishedAt);
  const rep = run?.iphoneBackup;
  const devices = rep?.devices ?? [];
  const footer: [string, string][] = [];
  if ((rep?.archived ?? 0n) > 0n) footer.push(["Archived copies", formatCount(rep?.archived ?? 0n)]);
  footer.push(["Next check", nextRun(overview.activity?.nextIphoneBackupCheckAt)]);
  return (
    <Card>
      <CardHeader
        image={<PhoneRegular fontSize={20} />}
        header={<Subtitle2 as="h2">iPhone backups</Subtitle2>}
        action={
          <Badge appearance="tint" color={summary.color}>
            {summary.label}
          </Badge>
        }
      />
      {devices.length === 0 && summary.detail !== "" && <Text>{summary.detail}</Text>}
      {devices.map((d) => {
        const done = toDate(d.completedAt);
        const read = toDate(d.lastFullReadAt);
        const badge = iphoneStateBadge[d.state];
        const facts: [string, ReactNode][] = [
          [
            "Device",
            <>
              {d.name} · iOS {d.productVersion}{" "}
              <Badge appearance="tint" color={badge.color}>
                {badge.label}
              </Badge>
            </>,
          ],
          ["Last backup", done === undefined ? "Never finished" : `${formatWhen(done)} (${formatRelative(done)})`],
          [
            "Size",
            `${formatBytes(d.sizeBytes)} · ${formatCount(d.files)} files · ${d.encrypted ? "encrypted" : "not encrypted"}`,
          ],
          ["Every file read", read === undefined ? "Not yet" : formatWhen(read)],
        ];
        if (d.reasons.length > 0) facts.push(["Problems", d.reasons.join("; ")]);
        return <Facts key={d.dir} facts={facts} />;
      })}
      {footer.length > 0 && <Facts facts={footer} />}
      <Caption1 className={styles.muted}>
        Apple&apos;s Devices app makes the backups; homecloud only reads them and alerts when one is old, unencrypted,
        unfinished or damaged.
        {checked !== undefined && ` Checked ${formatWhen(checked)}.`}
      </Caption1>
    </Card>
  );
}

function ICloudDriveCard({ overview, env }: { overview: Overview; env: Env }) {
  const styles = useStyles();
  const run = overview.lastDriveSync;
  const rep = run?.driveSync;
  const totals = overview.icloudDrive;
  const outcome = driveSyncOutcomeBadge[rep?.outcome ?? DriveSyncOutcome.UNSPECIFIED];
  const synced = toDate(run?.finishedAt);
  const signedIn = toDate(totals?.signedInAt);
  const sessionProblem = totals?.sessionProblem ?? "";

  const facts: [string, ReactNode][] = [
    ["Archived", `${formatCount(totals?.files ?? 0n)} files · ${formatBytes(totals?.bytes ?? 0n)}`],
  ];
  if ((totals?.deletedInIcloud ?? 0n) > 0n) {
    facts.push(["Deleted in iCloud", `${formatCount(totals?.deletedInIcloud ?? 0n)} (copies kept)`]);
  }
  if (synced !== undefined && rep !== undefined) {
    if (driveSyncListed(rep)) {
      const changes = [
        [rep.archived, "new"],
        [rep.changed, "changed"],
        [rep.moved, "moved"],
        [rep.deleted, "deleted in iCloud"],
        [rep.restored, "back in iCloud"],
        [rep.repaired, "repaired"],
        [rep.failed, "failed"],
      ]
        .filter(([n]) => (n as bigint) > 0n)
        .map(([n, what]) => `${formatCount(n as bigint)} ${what as string}`);
      facts.push(["Last sync", `${formatWhen(synced)} · ${changes.length === 0 ? "no changes" : changes.join(", ")}`]);
    } else {
      facts.push(["Last sync", `${formatWhen(synced)} · ${outcome.label}`]);
    }
  }
  facts.push(["Next sync", nextRun(overview.activity?.nextDriveSyncAt)]);
  facts.push([
    "Signed in",
    sessionProblem !== ""
      ? `The saved session can't be read: ${sessionProblem}`
      : signedIn === undefined
        ? "Never"
        : formatWhen(signedIn),
  ]);
  const needsSignIn =
    rep?.outcome === DriveSyncOutcome.SIGN_IN_REQUIRED || signedIn === undefined || sessionProblem !== "";

  return (
    <Card>
      <CardHeader
        image={<CloudArchiveRegular fontSize={20} />}
        header={<Subtitle2 as="h2">iCloud Drive</Subtitle2>}
        action={
          <Badge appearance="tint" color={outcome.color}>
            {outcome.label}
          </Badge>
        }
      />
      <Facts facts={facts} />
      {needsSignIn && <DriveSignInDialog env={env} />}
      <Caption1 className={styles.muted}>
        Every file is checked against Apple&apos;s own checksum. Files are never deleted from the archive: replaced
        versions are kept, and files deleted in iCloud stay.
        {rep !== undefined &&
          rep.unlistedApps > 0n &&
          ` ${formatCount(rep.unlistedApps)} apps keep no files in iCloud Drive.`}
      </Caption1>
    </Card>
  );
}

function scrubBadge(run: Run | undefined): { label: string; color: BadgeColor } {
  const mismatched = run?.scrub?.mismatched ?? 0n;
  const damaged = run?.scrub?.drive?.damaged ?? 0n;
  switch (run?.result) {
    case undefined:
    case RunResult.UNSPECIFIED:
    case RunResult.RUNNING:
      return { label: "Not checked yet", color: "informative" };
    case RunResult.OK:
      return { label: "All files match", color: "success" };
    case RunResult.FAILED:
      if (mismatched + damaged > 0n) {
        return { label: `${formatCount(mismatched + damaged)} changed`, color: "danger" };
      }
      return { label: "Incomplete", color: "warning" };
  }
}

function IntegrityCard({ overview, settings }: { overview: Overview; settings: Settings | undefined }) {
  const styles = useStyles();
  const scrub = overview.lastScrub;
  const backup = overview.lastLedgerBackup;
  const badge = scrubBadge(scrub);
  const scrubbed = toDate(scrub?.finishedAt);
  const backedUp = toDate(backup?.finishedAt);

  const facts: [string, string][] = [];
  if (scrubbed !== undefined && scrub?.scrub !== undefined) {
    const r = scrub.scrub;
    facts.push([
      "Last check",
      r.error === ""
        ? `${formatWhen(scrubbed)} · ${formatCount(r.checked)} of ${formatCount(r.eligible)} photo files`
        : `${formatWhen(scrubbed)} · couldn't run`,
    ]);
    const d = r.drive;
    if (r.error === "" && d !== undefined && d.eligible > 0n) {
      facts.push([
        "iCloud Drive",
        `${formatCount(d.checked)} of ${formatCount(d.eligible)} files` +
          (d.damaged > 0n ? ` · ${formatCount(d.damaged)} damaged` : "") +
          (d.errored > 0n ? ` · ${formatCount(d.errored)} not checked` : ""),
      ]);
    }
  }
  facts.push(["Next check", nextRun(overview.activity?.nextScrubAt)]);
  if (settings !== undefined) {
    // Each run re-reads the files confirmed longest ago, so the whole archive
    // is covered once every ceil(1 / fraction) runs. The agent rejects a
    // fraction outside (0, 1]; anything else here is not a schedule.
    if (settings.scrubFraction > 0 && settings.scrubFraction <= 1) {
      const runs = BigInt(Math.ceil(1 / settings.scrubFraction));
      facts.push(["Whole archive", `re-read ${formatInterval(settings.scrubIntervalSeconds * runs)}`]);
    }
  }
  facts.push([
    "Records backup",
    backedUp === undefined || backup?.ledgerBackup === undefined
      ? "Not yet"
      : backup.result === RunResult.OK
        ? `${formatWhen(backedUp)} · ${formatBytes(backup.ledgerBackup.sizeBytes)}`
        : `${formatWhen(backedUp)} · failed`,
  ]);

  return (
    <Card>
      <CardHeader
        image={<ShieldCheckmarkRegular fontSize={20} />}
        header={<Subtitle2 as="h2">Integrity</Subtitle2>}
        action={
          <Badge appearance="tint" color={badge.color}>
            {badge.label}
          </Badge>
        }
      />
      <Facts facts={facts} />
      <Caption1 className={styles.muted}>
        Archived files are re-read and compared with the checksum recorded at download; a photo that changed is
        quarantined, an iCloud Drive file downloaded again, and you&apos;re alerted. The archive&apos;s records are
        copied to another drive.
      </Caption1>
    </Card>
  );
}
