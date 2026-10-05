import { useMutation, useQuery } from "@connectrpc/connect-query";
import {
  Badge,
  Button,
  Caption1,
  Card,
  CardHeader,
  Dialog,
  DialogActions,
  DialogBody,
  DialogContent,
  DialogSurface,
  DialogTitle,
  DialogTrigger,
  Link,
  makeStyles,
  MessageBar,
  MessageBarBody,
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
  CopyRegular,
  HardDriveRegular,
  HeartPulseRegular,
  KeyRegular,
  PlugDisconnectedRegular,
  ShieldErrorRegular,
} from "@fluentui/react-icons";
import { Link as RouterLink, useNavigate } from "@tanstack/react-router";
import { Fragment, useEffect, useState, type ReactNode } from "react";

import { useOverview } from "../api/overviewContext";
import { usePlatform } from "../app/platformContext";
import { Page } from "../components/Page";
import { runRow } from "../components/runs";
import { useNotify } from "../components/toast";
import { formatBytes, formatCount, formatInterval, formatRelative, formatWhen, toDate } from "../format";
import {
  AgentService,
  AppleAccountState,
  DriveHealthState,
  Phase,
  type DriveHealth,
  type Overview,
  type Settings,
} from "../gen/homecloud/agent/v1/agent_pb";

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
  command: {
    display: "flex",
    alignItems: "center",
    gap: tokens.spacingHorizontalS,
    padding: tokens.spacingHorizontalS,
    borderRadius: tokens.borderRadiusMedium,
    backgroundColor: tokens.colorNeutralBackground3,
    fontFamily: tokens.fontFamilyMonospace,
    wordBreak: "break-all",
  },
  tableBox: { overflowX: "auto" },
  nowrap: { whiteSpace: "nowrap" },
  // Fluent tables default to equal fixed-width columns; size columns to their content instead.
  table: { tableLayout: "auto" },
  offlineActions: { display: "flex", flexWrap: "wrap", justifyContent: "center", gap: tokens.spacingHorizontalM },
});

export function OverviewPage() {
  const styles = useStyles();
  const { overview, connection, receivedAt, reconnect } = useOverview();
  const settings = useQuery(AgentService.method.getSettings, {});
  const notify = useNotify();
  const startSync = useMutation(AgentService.method.startSync, {
    onSuccess: (res) => {
      if (!res.started) {
        notify("info", "A sync is already running");
      }
    },
    onError: (err) => {
      notify("error", "Couldn't start a sync", err.message);
    },
  });

  const busy = overview?.activity?.phase !== undefined && overview.activity.phase !== Phase.IDLE;
  const actions = (
    <Button
      appearance="primary"
      icon={<ArrowSyncRegular />}
      disabled={connection !== "live" || busy || startSync.isPending}
      onClick={() => {
        startSync.mutate({});
      }}
    >
      {busy ? "Syncing…" : "Sync now"}
    </Button>
  );

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
  return (
    <Page title="Overview" actions={actions}>
      <PhaseBanner overview={overview} />
      <Hero overview={overview} settings={settings.data?.settings} />
      <Stats overview={overview} />
      <div className={styles.cards}>
        <StorageCard overview={overview} />
        <AccountCard overview={overview} />
        <ScheduleCard overview={overview} settings={settings.data?.settings} />
        <DriveCard drive={overview.drive} />
      </div>
      <RecentSyncs lastCycleId={overview.lastCycle?.id} />
    </Page>
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
      <Text className={styles.muted}>
        The agent downloads and checks your files in the background and starts when you sign in. Your files on disk are
        not affected. It may have stopped, or still be starting.
      </Text>
      <div className={styles.offlineActions}>
        {platform.kind === "desktop" && (
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

function Hero({ overview, settings }: { overview: Overview; settings: Settings | undefined }) {
  const styles = useStyles();
  const a = overview.assets;
  const quarantined = a?.quarantined ?? 0n;
  const pending = (a?.discovered ?? 0n) + (a?.downloaded ?? 0n);
  const account = overview.appleAccount?.state ?? AppleAccountState.UNSPECIFIED;

  let tone: "success" | "warning" | "danger" | "brand" = "success";
  let icon: ReactNode = <CheckmarkCircleRegular />;
  let title = "Your archive is complete and verified";
  let body: ReactNode = `Every item in iCloud Photos is in ${overview.storage?.root ?? "the archive"} and passed its checks.`;
  let action: ReactNode = null;

  const drive = overview.drive?.state ?? DriveHealthState.UNSPECIFIED;

  if (drive === DriveHealthState.FAILING || drive === DriveHealthState.WARNING) {
    tone = "danger";
    icon = <HardDriveRegular />;
    title =
      drive === DriveHealthState.FAILING
        ? "Your archive drive expects to fail"
        : "Your archive drive reports a problem";
    body = `${overview.drive?.reasons.join("; ") ?? ""}. It holds your only copy: replace it soon, and keep iCloud cleanup off until then.`;
  } else if (account === AppleAccountState.SIGN_IN_REQUIRED || account === AppleAccountState.NEEDS_ATTENTION) {
    tone = "warning";
    icon = <KeyRegular />;
    title =
      account === AppleAccountState.SIGN_IN_REQUIRED ? "Apple sign-in needed" : "Your Apple account needs attention";
    body =
      "New photos aren't being downloaded until you sign in again. Everything already archived is safe, and nothing is deleted from iCloud meanwhile.";
    action = <SignInDialog settings={settings} />;
  } else if (quarantined > 0n) {
    tone = "danger";
    icon = <ShieldErrorRegular />;
    title = `${formatCount(quarantined)} ${quarantined === 1n ? "item" : "items"} failed verification`;
    body = "They're kept aside and will never be deleted from iCloud until you've reviewed them.";
    action = <ReviewButton />;
  } else if (pending > 0n) {
    tone = "brand";
    icon = <ClockRegular />;
    title = `${formatCount(pending)} ${pending === 1n ? "item is" : "items are"} still being archived`;
    body = "They are downloaded and checked during the next sync.";
  }

  const palette = {
    success: [tokens.colorPaletteGreenBackground1, tokens.colorPaletteGreenForeground1],
    warning: [tokens.colorPaletteYellowBackground1, tokens.colorPaletteDarkOrangeForeground1],
    danger: [tokens.colorPaletteRedBackground1, tokens.colorPaletteRedForeground1],
    brand: [tokens.colorBrandBackground2, tokens.colorBrandForeground1],
  }[tone];

  return (
    <Card className={styles.hero}>
      <div className={styles.heroIcon} style={{ backgroundColor: palette[0], color: palette[1] }}>
        {icon}
      </div>
      <div className={styles.heroText}>
        <Title3 as="h2">{title}</Title3>
        <Text>{body}</Text>
      </div>
      {action}
    </Card>
  );
}

function SignInDialog({ settings }: { settings: Settings | undefined }) {
  const styles = useStyles();
  const notify = useNotify();
  const command =
    settings === undefined
      ? undefined
      : `"${settings.downloader?.binary ?? ""}" login --config "${settings.downloaderConfig}"`;
  return (
    <Dialog>
      <DialogTrigger disableButtonEnhancement>
        <Button appearance="primary">Sign in…</Button>
      </DialogTrigger>
      <DialogSurface>
        <DialogBody>
          <DialogTitle>Sign in to iCloud again</DialogTitle>
          <DialogContent>
            <Text block>
              Run this in a terminal on this computer. Your password and the code from your iPhone go straight to Apple;
              homecloud never sees them.
            </Text>
            {command === undefined ? (
              <Spinner size="tiny" />
            ) : (
              <div className={styles.command}>
                <span style={{ flex: 1 }}>{command}</span>
                <Button
                  icon={<CopyRegular />}
                  aria-label="Copy command"
                  onClick={() => {
                    navigator.clipboard.writeText(command).then(
                      () => {
                        notify("success", "Command copied");
                      },
                      () => {
                        notify("error", "Couldn't copy the command");
                      },
                    );
                  }}
                />
              </div>
            )}
            <Text block className={styles.muted}>
              Downloads resume at the next sync.
            </Text>
          </DialogContent>
          <DialogActions>
            <DialogTrigger disableButtonEnhancement>
              <Button>Close</Button>
            </DialogTrigger>
          </DialogActions>
        </DialogBody>
      </DialogSurface>
    </Dialog>
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

const accountBadge: Record<
  AppleAccountState,
  { label: string; color: "success" | "warning" | "danger" | "informative" }
> = {
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
  const runs = useQuery(AgentService.method.listRuns, { pageSize: 5 });
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

const driveBadge: Record<DriveHealthState, { label: string; color: "success" | "warning" | "danger" | "informative" }> =
  {
    [DriveHealthState.UNSPECIFIED]: { label: "Not checked yet", color: "informative" },
    [DriveHealthState.OK]: { label: "Healthy", color: "success" },
    [DriveHealthState.WARNING]: { label: "Warning", color: "warning" },
    [DriveHealthState.FAILING]: { label: "Failing", color: "danger" },
    [DriveHealthState.UNAVAILABLE]: { label: "Unavailable", color: "informative" },
  };

function DriveCard({ drive }: { drive: DriveHealth | undefined }) {
  const styles = useStyles();
  const badge = driveBadge[drive?.state ?? DriveHealthState.UNSPECIFIED];
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
      {facts.length > 0 && (
        <div className={styles.facts}>
          {facts.map(([k, v]) => (
            <Fragment key={k}>
              <Text className={styles.muted}>{k}</Text>
              <Text>{v}</Text>
            </Fragment>
          ))}
        </div>
      )}
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
