import { useInfiniteQuery } from "@connectrpc/connect-query";
import {
  Badge,
  Button,
  Card,
  Dropdown,
  Field,
  makeStyles,
  Option,
  Spinner,
  Tab,
  Table,
  TableBody,
  TableCell,
  TableHeader,
  TableHeaderCell,
  TableRow,
  TabList,
  Text,
  tokens,
} from "@fluentui/react-components";
import { useState } from "react";

import { LoadError } from "../components/LoadError";
import { Page } from "../components/Page";
import { iphoneStateBadge, stateLabel, tierLabel } from "../components/labels";
import { runRow } from "../components/runs";
import { formatBytes, formatCount, formatDuration, formatWhen, toDate } from "../format";
import {
  AgentService,
  DriveHealthState,
  EventKind,
  RunKind,
  RunResult,
  type Event,
  type Run,
} from "../gen/homecloud/agent/v1/agent_pb";

const driveStateLabel: Record<DriveHealthState, string> = {
  [DriveHealthState.UNSPECIFIED]: "Unknown",
  [DriveHealthState.OK]: "Healthy",
  [DriveHealthState.WARNING]: "Warning",
  [DriveHealthState.FAILING]: "Failing",
  [DriveHealthState.UNAVAILABLE]: "Health unavailable",
};

const pageSize = 25;

const useStyles = makeStyles({
  tableBox: { overflowX: "auto" },
  num: { textAlign: "right", fontVariantNumeric: "tabular-nums" },
  // Header cells wrap their label in a flex container (the `button` slot); align it like the values.
  numHead: { justifyContent: "flex-end" },
  // Fluent tables default to equal fixed-width columns; size columns to their content instead.
  table: { tableLayout: "auto" },
  nowrap: { whiteSpace: "nowrap" },
  detail: { whiteSpace: "normal", minWidth: "260px" },
  mono: { fontFamily: tokens.fontFamilyMonospace, fontSize: tokens.fontSizeBase200 },
  more: { alignSelf: "flex-start" },
  muted: { color: tokens.colorNeutralForeground3 },
  filters: { display: "flex", flexWrap: "wrap", gap: tokens.spacingHorizontalM },
});

const tabs = {
  runs: "Syncs",
  integrity: "Integrity checks",
  backups: "Records backups",
  iphone: "iPhone backups",
  events: "Events",
} as const;
type ActivityTab = keyof typeof tabs;

function isTab(v: unknown): v is ActivityTab {
  return typeof v === "string" && Object.hasOwn(tabs, v);
}

export function ActivityPage() {
  const [tab, setTab] = useState<ActivityTab>("runs");
  return (
    <Page title="Activity">
      <TabList
        selectedValue={tab}
        onTabSelect={(_, d) => {
          if (isTab(d.value)) {
            setTab(d.value);
          }
        }}
      >
        {Object.entries(tabs).map(([value, label]) => (
          <Tab key={value} value={value}>
            {label}
          </Tab>
        ))}
      </TabList>
      {tab === "runs" && <Runs />}
      {tab === "integrity" && <ScrubRuns />}
      {tab === "backups" && <BackupRuns />}
      {tab === "iphone" && <IPhoneBackupRuns />}
      {tab === "events" && <Events />}
    </Page>
  );
}

/** How long a finished run took. */
function took(run: Run): string {
  const started = toDate(run.startedAt);
  const finished = toDate(run.finishedAt);
  return started !== undefined && finished !== undefined ? formatDuration(finished.getTime() - started.getTime()) : "—";
}

function started(run: Run): string {
  const at = toDate(run.startedAt);
  return at === undefined ? "—" : formatWhen(at);
}

function nextPage(last: { nextPageToken: string }): string | undefined {
  return last.nextPageToken === "" ? undefined : last.nextPageToken;
}

function MoreButton({ q }: { q: { hasNextPage: boolean; isFetchingNextPage: boolean; fetchNextPage: () => unknown } }) {
  const styles = useStyles();
  if (!q.hasNextPage) {
    return null;
  }
  return (
    <Button
      className={styles.more}
      disabled={q.isFetchingNextPage}
      onClick={() => {
        void q.fetchNextPage();
      }}
    >
      {q.isFetchingNextPage ? "Loading…" : "Show older"}
    </Button>
  );
}

function Runs() {
  const styles = useStyles();
  const q = useInfiniteQuery(
    AgentService.method.listRuns,
    { kind: RunKind.CYCLE, pageSize, pageToken: "" },
    { pageParamKey: "pageToken", getNextPageParam: nextPage },
  );
  if (q.isPending) {
    return <Spinner label="Loading syncs…" />;
  }
  if (q.isError) {
    return <LoadError what="syncs" error={q.error} onRetry={() => void q.refetch()} />;
  }
  const rows = q.data.pages.flatMap((p) => p.runs).map(runRow);
  return (
    <>
      <Card className={styles.tableBox}>
        <Table size="small" aria-label="Syncs" className={styles.table}>
          <TableHeader>
            <TableRow>
              <TableHeaderCell>Started</TableHeaderCell>
              <TableHeaderCell>Download</TableHeaderCell>
              <TableHeaderCell button={{ className: styles.numHead }}>In iCloud</TableHeaderCell>
              <TableHeaderCell button={{ className: styles.numHead }}>New</TableHeaderCell>
              <TableHeaderCell button={{ className: styles.numHead }}>Changed</TableHeaderCell>
              <TableHeaderCell button={{ className: styles.numHead }}>Checked</TableHeaderCell>
              <TableHeaderCell button={{ className: styles.numHead }}>Failed</TableHeaderCell>
              <TableHeaderCell button={{ className: styles.numHead }}>Took</TableHeaderCell>
              <TableHeaderCell>Result</TableHeaderCell>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((r) => (
              <TableRow key={r.id}>
                <TableCell className={styles.nowrap}>{r.started}</TableCell>
                <TableCell>{r.download}</TableCell>
                <TableCell className={styles.num}>{r.inICloud}</TableCell>
                <TableCell className={styles.num}>{r.added}</TableCell>
                <TableCell className={styles.num}>{r.changed}</TableCell>
                <TableCell className={styles.num}>{r.checked}</TableCell>
                <TableCell className={styles.num}>{r.failed}</TableCell>
                <TableCell className={styles.num}>{r.took}</TableCell>
                <TableCell>{r.result}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
        {rows.length === 0 && <Text className={styles.muted}>No syncs yet.</Text>}
      </Card>
      <MoreButton q={q} />
    </>
  );
}

function ScrubRuns() {
  const styles = useStyles();
  const q = useInfiniteQuery(
    AgentService.method.listRuns,
    { kind: RunKind.SCRUB, pageSize, pageToken: "" },
    { pageParamKey: "pageToken", getNextPageParam: nextPage },
  );
  if (q.isPending) {
    return <Spinner label="Loading integrity checks…" />;
  }
  if (q.isError) {
    return <LoadError what="integrity checks" error={q.error} onRetry={() => void q.refetch()} />;
  }
  const runs = q.data.pages.flatMap((p) => p.runs);
  return (
    <>
      <Card className={styles.tableBox}>
        <Table size="small" aria-label="Integrity checks" className={styles.table}>
          <TableHeader>
            <TableRow>
              <TableHeaderCell>Started</TableHeaderCell>
              <TableHeaderCell button={{ className: styles.numHead }}>Re-read</TableHeaderCell>
              <TableHeaderCell button={{ className: styles.numHead }}>Changed</TableHeaderCell>
              <TableHeaderCell button={{ className: styles.numHead }}>Not checked</TableHeaderCell>
              <TableHeaderCell button={{ className: styles.numHead }}>Took</TableHeaderCell>
              <TableHeaderCell>Result</TableHeaderCell>
            </TableRow>
          </TableHeader>
          <TableBody>
            {runs.map((run) => {
              const r = run.scrub;
              return (
                <TableRow key={run.id.toString()}>
                  <TableCell className={styles.nowrap}>{started(run)}</TableCell>
                  <TableCell className={styles.num}>
                    {r === undefined ? "—" : `${formatCount(r.checked)} of ${formatCount(r.eligible)}`}
                  </TableCell>
                  <TableCell className={styles.num}>{r === undefined ? "—" : formatCount(r.mismatched)}</TableCell>
                  <TableCell className={styles.num}>{r === undefined ? "—" : formatCount(r.errored)}</TableCell>
                  <TableCell className={styles.num}>{took(run)}</TableCell>
                  <TableCell className={styles.detail}>
                    <RunBadge
                      run={run}
                      ok="All match"
                      failed={
                        r?.error !== ""
                          ? "Couldn't run"
                          : r.mismatched > 0n
                            ? `${formatCount(r.mismatched)} quarantined`
                            : `${formatCount(r.errored)} not checked`
                      }
                    />
                    {r !== undefined && r.error !== "" && <Text className={styles.muted}> {r.error}</Text>}
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
        {runs.length === 0 && <Text className={styles.muted}>No integrity check has run yet.</Text>}
      </Card>
      <MoreButton q={q} />
    </>
  );
}

function BackupRuns() {
  const styles = useStyles();
  const q = useInfiniteQuery(
    AgentService.method.listRuns,
    { kind: RunKind.LEDGER_BACKUP, pageSize, pageToken: "" },
    { pageParamKey: "pageToken", getNextPageParam: nextPage },
  );
  if (q.isPending) {
    return <Spinner label="Loading records backups…" />;
  }
  if (q.isError) {
    return <LoadError what="records backups" error={q.error} onRetry={() => void q.refetch()} />;
  }
  const runs = q.data.pages.flatMap((p) => p.runs);
  return (
    <>
      <Card className={styles.tableBox}>
        <Table size="small" aria-label="Records backups" className={styles.table}>
          <TableHeader>
            <TableRow>
              <TableHeaderCell>Started</TableHeaderCell>
              <TableHeaderCell button={{ className: styles.numHead }}>Size</TableHeaderCell>
              <TableHeaderCell button={{ className: styles.numHead }}>Older copies removed</TableHeaderCell>
              <TableHeaderCell button={{ className: styles.numHead }}>Took</TableHeaderCell>
              <TableHeaderCell>Result</TableHeaderCell>
            </TableRow>
          </TableHeader>
          <TableBody>
            {runs.map((run) => {
              const r = run.ledgerBackup;
              const ok = run.result === RunResult.OK;
              return (
                <TableRow key={run.id.toString()}>
                  <TableCell className={styles.nowrap}>{started(run)}</TableCell>
                  <TableCell className={styles.num}>
                    {r === undefined || !ok ? "—" : formatBytes(r.sizeBytes)}
                  </TableCell>
                  <TableCell className={styles.num}>{r === undefined || !ok ? "—" : formatCount(r.pruned)}</TableCell>
                  <TableCell className={styles.num}>{took(run)}</TableCell>
                  <TableCell className={styles.detail}>
                    <RunBadge run={run} ok="Saved" failed="Failed" />
                    {r !== undefined && r.error !== "" && <Text className={styles.muted}> {r.error}</Text>}
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
        {runs.length === 0 && <Text className={styles.muted}>No records backup has been made yet.</Text>}
      </Card>
      <MoreButton q={q} />
    </>
  );
}

function IPhoneBackupRuns() {
  const styles = useStyles();
  const q = useInfiniteQuery(
    AgentService.method.listRuns,
    { kind: RunKind.IPHONE_BACKUP, pageSize, pageToken: "" },
    { pageParamKey: "pageToken", getNextPageParam: nextPage },
  );
  if (q.isPending) {
    return <Spinner label="Loading iPhone backup checks…" />;
  }
  if (q.isError) {
    return <LoadError what="iPhone backup checks" error={q.error} onRetry={() => void q.refetch()} />;
  }
  const runs = q.data.pages.flatMap((p) => p.runs);
  return (
    <>
      <Card className={styles.tableBox}>
        <Table size="small" aria-label="iPhone backup checks" className={styles.table}>
          <TableHeader>
            <TableRow>
              <TableHeaderCell>Checked</TableHeaderCell>
              <TableHeaderCell>Devices</TableHeaderCell>
              <TableHeaderCell button={{ className: styles.numHead }}>Took</TableHeaderCell>
              <TableHeaderCell>Result</TableHeaderCell>
            </TableRow>
          </TableHeader>
          <TableBody>
            {runs.map((run) => {
              const r = run.iphoneBackup;
              return (
                <TableRow key={run.id.toString()}>
                  <TableCell className={styles.nowrap}>{started(run)}</TableCell>
                  <TableCell className={styles.detail}>
                    {r === undefined
                      ? "—"
                      : r.devices.length === 0
                        ? r.error === ""
                          ? "No backup found"
                          : "—"
                        : r.devices.map((d) => (
                            <div key={d.udid}>
                              {d.name}{" "}
                              <Badge appearance="tint" color={iphoneStateBadge[d.state].color}>
                                {iphoneStateBadge[d.state].label}
                              </Badge>
                              {d.reasons.length > 0 && <Text className={styles.muted}> {d.reasons.join("; ")}</Text>}
                            </div>
                          ))}
                  </TableCell>
                  <TableCell className={styles.num}>{took(run)}</TableCell>
                  <TableCell className={styles.detail}>
                    <RunBadge run={run} ok="Healthy" failed="Needs attention" />
                    {r !== undefined && r.error !== "" && <Text className={styles.muted}> {r.error}</Text>}
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
        {runs.length === 0 && <Text className={styles.muted}>The iPhone backups haven&apos;t been checked yet.</Text>}
      </Card>
      <MoreButton q={q} />
    </>
  );
}

function RunBadge({ run, ok, failed }: { run: Run; ok: string; failed: string }) {
  switch (run.result) {
    case RunResult.RUNNING:
      return (
        <Badge appearance="tint" color="brand">
          Running
        </Badge>
      );
    case RunResult.OK:
      return (
        <Badge appearance="tint" color="success">
          {ok}
        </Badge>
      );
    case RunResult.FAILED:
      return (
        <Badge appearance="tint" color="danger">
          {failed}
        </Badge>
      );
    case RunResult.UNSPECIFIED:
      return null;
  }
}

const filters = {
  all: {
    label: "All events",
    kinds: [
      EventKind.RENDITION_CHANGED,
      EventKind.RENDITION_MOVED,
      EventKind.QUARANTINED,
      EventKind.QUARANTINE_RELEASED,
      EventKind.DRIVE_HEALTH_CHANGED,
      EventKind.IPHONE_BACKUP_CHANGED,
    ],
  },
  quarantined: { label: "Quarantined", kinds: [EventKind.QUARANTINED] },
  released: { label: "Released", kinds: [EventKind.QUARANTINE_RELEASED] },
  changed: { label: "Changed in iCloud", kinds: [EventKind.RENDITION_CHANGED] },
  moved: { label: "Moved", kinds: [EventKind.RENDITION_MOVED] },
  drive: { label: "Drive health", kinds: [EventKind.DRIVE_HEALTH_CHANGED] },
  iphone: { label: "iPhone backups", kinds: [EventKind.IPHONE_BACKUP_CHANGED] },
  states: { label: "State changes", kinds: [EventKind.STATE_CHANGED] },
} as const;
type Filter = keyof typeof filters;

function isFilter(v: string | undefined): v is Filter {
  return v !== undefined && Object.hasOwn(filters, v);
}

function describe(e: Event): { kind: string; color: "danger" | "success" | "informative"; text: string } {
  switch (e.detail.case) {
    case "renditionChanged":
      return {
        kind: "Changed in iCloud",
        color: "informative",
        text: `The ${e.detail.value.version} version changed in iCloud. It was downloaded again and its checks are redone.`,
      };
    case "quarantined":
      return {
        kind: "Quarantined",
        color: "danger",
        text: `${tierLabel[e.detail.value.tier]} check failed: ${e.detail.value.reason}`,
      };
    case "quarantineReleased":
      return { kind: "Released", color: "success", text: `Reason: ${e.detail.value.reason}` };
    case "stateChanged":
      return {
        kind: "State changed",
        color: "informative",
        text: `${stateLabel[e.detail.value.from]} → ${stateLabel[e.detail.value.to]}`,
      };
    case "renditionMoved":
      return {
        kind: "Moved",
        color: "informative",
        text: `The downloader now keeps the ${e.detail.value.version} version at ${e.detail.value.to} (was ${e.detail.value.from}). It is checked again there.`,
      };
    case "driveHealthChanged":
      return {
        kind: "Drive health",
        color:
          e.detail.value.to === DriveHealthState.OK
            ? "success"
            : e.detail.value.to === DriveHealthState.UNAVAILABLE
              ? "informative"
              : "danger",
        text: [driveStateLabel[e.detail.value.to], ...e.detail.value.reasons].join(": "),
      };
    case "iphoneBackupChanged": {
      const c = e.detail.value;
      const tone = iphoneStateBadge[c.to].color;
      return {
        kind: "iPhone backup",
        color: tone === "success" ? "success" : tone === "informative" ? "informative" : "danger",
        text: [`${c.deviceName}: ${iphoneStateBadge[c.to].label}`, ...c.reasons].join(": "),
      };
    }
    case undefined:
      return { kind: "Event", color: "informative", text: "" };
  }
}

function Events() {
  const styles = useStyles();
  const [filter, setFilter] = useState<Filter>("all");
  const q = useInfiniteQuery(
    AgentService.method.listEvents,
    { kinds: [...filters[filter].kinds], pageSize, pageToken: "" },
    { pageParamKey: "pageToken", getNextPageParam: nextPage },
  );
  const events = q.data?.pages.flatMap((p) => p.events) ?? [];
  return (
    <>
      <div className={styles.filters}>
        <Field label="Show">
          <Dropdown
            value={filters[filter].label}
            selectedOptions={[filter]}
            onOptionSelect={(_, d) => {
              if (isFilter(d.optionValue)) {
                setFilter(d.optionValue);
              }
            }}
          >
            {Object.entries(filters).map(([key, f]) => (
              <Option key={key} value={key}>
                {f.label}
              </Option>
            ))}
          </Dropdown>
        </Field>
      </div>
      {q.isPending ? (
        <Spinner label="Loading events…" />
      ) : q.isError ? (
        <LoadError what="events" error={q.error} onRetry={() => void q.refetch()} />
      ) : (
        <Card className={styles.tableBox}>
          <Table size="small" aria-label="Events" className={styles.table}>
            <TableHeader>
              <TableRow>
                <TableHeaderCell>When</TableHeaderCell>
                <TableHeaderCell>Event</TableHeaderCell>
                <TableHeaderCell>Item</TableHeaderCell>
                <TableHeaderCell>Detail</TableHeaderCell>
              </TableRow>
            </TableHeader>
            <TableBody>
              {events.map((e) => {
                const d = describe(e);
                const at = toDate(e.at);
                return (
                  <TableRow key={e.id.toString()}>
                    <TableCell className={styles.nowrap}>{at === undefined ? "—" : formatWhen(at)}</TableCell>
                    <TableCell>
                      <Badge appearance="tint" color={d.color}>
                        {d.kind}
                      </Badge>
                    </TableCell>
                    <TableCell className={styles.mono}>{e.asset?.filename ?? ""}</TableCell>
                    <TableCell className={styles.detail}>{d.text}</TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
          {events.length === 0 && <Text className={styles.muted}>Nothing recorded yet.</Text>}
        </Card>
      )}
      <MoreButton q={q} />
      <Text size={200} className={styles.muted}>
        The event log is append-only: entries can&apos;t be edited or removed, including by homecloud itself.
      </Text>
    </>
  );
}
