import { useInfiniteQuery, useQuery } from "@connectrpc/connect-query";
import {
  Badge,
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
import { MoreButton, RunBadge, RunTable } from "../components/RunTable";
import { nextPage, useTableStyles } from "../components/tables";
import {
  driveFileChangeBadge,
  driveHealthBadge,
  driveSyncListed,
  driveSyncOutcomeBadge,
  iphoneStateBadge,
  stateLabel,
  tierLabel,
  type BadgeColor,
} from "../components/labels";
import { runRow } from "../components/runs";
import { formatBytes, formatCount, formatWhen, toDate } from "../format";
import {
  AgentService,
  DriveFileChange,
  DriveSyncOutcome,
  EventKind,
  RunKind,
  type DriveFileEvent,
  type Event,
  type Run,
} from "../gen/homecloud/agent/v2/agent_pb";

const useStyles = makeStyles({
  filters: { display: "flex", flexWrap: "wrap", gap: tokens.spacingHorizontalM },
});

const tabs = {
  runs: "Syncs",
  integrity: "Integrity checks",
  backups: "Records backups",
  iphone: "iPhone backups",
  drive: "iCloud Drive",
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
      {tab === "runs" && <CycleRuns />}
      {tab === "integrity" && <ScrubRuns />}
      {tab === "backups" && <BackupRuns />}
      {tab === "iphone" && <IPhoneBackupRuns />}
      {tab === "drive" && <DriveSyncRuns />}
      {tab === "events" && <Events />}
    </Page>
  );
}

const dash = "—";

function CycleRuns() {
  return (
    <RunTable
      kind={RunKind.CYCLE}
      noun="syncs"
      empty="No syncs yet."
      columns={[
        { header: "Download", cell: (run) => runRow(run).download },
        { header: "In iCloud", numeric: true, cell: (run) => runRow(run).inICloud },
        { header: "New", numeric: true, cell: (run) => runRow(run).added },
        { header: "Changed", numeric: true, cell: (run) => runRow(run).changed },
        { header: "Checked", numeric: true, cell: (run) => runRow(run).checked },
        { header: "Failed", numeric: true, cell: (run) => runRow(run).failed },
      ]}
      result={(run) => runRow(run).result}
    />
  );
}

function ScrubRuns() {
  const styles = useTableStyles();
  const of = (checked: bigint, eligible: bigint) => `${formatCount(checked)} of ${formatCount(eligible)}`;
  return (
    <RunTable
      kind={RunKind.SCRUB}
      noun="integrity checks"
      empty="No integrity check has run yet."
      columns={[
        {
          header: "Photos re-read",
          numeric: true,
          cell: (run) => (run.scrub ? of(run.scrub.checked, run.scrub.eligible) : dash),
        },
        { header: "Changed", numeric: true, cell: (run) => (run.scrub ? formatCount(run.scrub.mismatched) : dash) },
        {
          header: "iCloud Drive re-read",
          numeric: true,
          cell: (run) => (run.scrub?.drive ? of(run.scrub.drive.checked, run.scrub.drive.eligible) : dash),
        },
        {
          header: "Damaged",
          numeric: true,
          cell: (run) => (run.scrub?.drive ? formatCount(run.scrub.drive.damaged) : dash),
        },
        {
          header: "Not checked",
          numeric: true,
          cell: (run) => (run.scrub ? formatCount(run.scrub.errored + (run.scrub.drive?.errored ?? 0n)) : dash),
        },
      ]}
      result={(run) => {
        const r = run.scrub;
        const problems: string[] = [];
        if (r !== undefined && r.mismatched > 0n) problems.push(`${formatCount(r.mismatched)} quarantined`);
        if (r?.drive !== undefined && r.drive.damaged > 0n)
          problems.push(`${formatCount(r.drive.damaged)} Drive files damaged`);
        const notChecked = r === undefined ? 0n : r.errored + (r.drive?.errored ?? 0n);
        if (notChecked > 0n) problems.push(`${formatCount(notChecked)} not checked`);
        return (
          <>
            <RunBadge
              run={run}
              ok="All match"
              failed={
                r !== undefined && r.error !== ""
                  ? "Couldn't run"
                  : problems.length === 0
                    ? "Failed"
                    : problems.join(", ")
              }
            />
            {r !== undefined && r.error !== "" && <Text className={styles.muted}> {r.error}</Text>}
          </>
        );
      }}
    />
  );
}

function BackupRuns() {
  const styles = useTableStyles();
  const saved = (run: Run) => run.ledgerBackup?.error === "";
  return (
    <RunTable
      kind={RunKind.LEDGER_BACKUP}
      noun="records backups"
      empty="No records backup has been made yet."
      columns={[
        {
          header: "Size",
          numeric: true,
          cell: (run) => (saved(run) ? formatBytes(run.ledgerBackup?.sizeBytes ?? 0n) : dash),
        },
        {
          header: "Older copies removed",
          numeric: true,
          cell: (run) => (saved(run) ? formatCount(run.ledgerBackup?.pruned ?? 0n) : dash),
        },
      ]}
      result={(run) => (
        <>
          <RunBadge run={run} ok="Saved" failed="Failed" />
          {run.ledgerBackup !== undefined && run.ledgerBackup.error !== "" && (
            <Text className={styles.muted}> {run.ledgerBackup.error}</Text>
          )}
        </>
      )}
    />
  );
}

function IPhoneBackupRuns() {
  const styles = useTableStyles();
  return (
    <RunTable
      kind={RunKind.IPHONE_BACKUP}
      noun="iPhone backup checks"
      empty="The iPhone backups haven't been checked yet."
      columns={[
        {
          header: "Backups",
          cell: (run) => {
            const r = run.iphoneBackup;
            if (r === undefined) return dash;
            if (r.devices.length === 0) return r.error === "" && r.dirErrors.length === 0 ? "No backup found" : dash;
            return r.devices.map((d) => (
              <div key={d.dir}>
                {d.name}{" "}
                <Badge appearance="tint" color={iphoneStateBadge[d.state].color}>
                  {iphoneStateBadge[d.state].label}
                </Badge>
                {d.reasons.length > 0 && <Text className={styles.muted}> {d.reasons.join("; ")}</Text>}
              </div>
            ));
          },
        },
        {
          header: "Archived copies",
          numeric: true,
          cell: (run) => (run.iphoneBackup ? formatCount(run.iphoneBackup.archived) : dash),
        },
      ]}
      result={(run) => {
        const r = run.iphoneBackup;
        return (
          <>
            <RunBadge run={run} ok="Checked" failed="Needs attention" />
            {r !== undefined && r.error !== "" && <Text className={styles.muted}> {r.error}</Text>}
            {r?.dirErrors.map((e) => (
              <Text key={e} block className={styles.muted}>
                {e}
              </Text>
            ))}
          </>
        );
      }}
    />
  );
}

function DriveSyncRuns() {
  const styles = useTableStyles();
  // Counts mean something only for a sync that got as far as listing.
  const count = (run: Run, n: (r: NonNullable<Run["driveSync"]>) => bigint) =>
    run.driveSync !== undefined && driveSyncListed(run.driveSync) ? formatCount(n(run.driveSync)) : dash;
  return (
    <RunTable
      kind={RunKind.DRIVE_SYNC}
      noun="iCloud Drive syncs"
      empty="iCloud Drive hasn't been synced yet."
      columns={[
        { header: "Listed", numeric: true, cell: (run) => count(run, (r) => r.listed) },
        { header: "New", numeric: true, cell: (run) => count(run, (r) => r.archived) },
        { header: "Changed", numeric: true, cell: (run) => count(run, (r) => r.changed) },
        { header: "Moved", numeric: true, cell: (run) => count(run, (r) => r.moved) },
        { header: "Deleted in iCloud", numeric: true, cell: (run) => count(run, (r) => r.deleted) },
        { header: "Back in iCloud", numeric: true, cell: (run) => count(run, (r) => r.restored) },
        { header: "Repaired", numeric: true, cell: (run) => count(run, (r) => r.repaired) },
        { header: "Failed", numeric: true, cell: (run) => count(run, (r) => r.failed) },
        {
          header: "Downloaded",
          numeric: true,
          cell: (run) =>
            run.driveSync !== undefined && driveSyncListed(run.driveSync)
              ? formatBytes(run.driveSync.downloadedBytes)
              : dash,
        },
      ]}
      result={(run) => {
        const r = run.driveSync;
        const outcome = r?.outcome ?? DriveSyncOutcome.UNSPECIFIED;
        return (
          <>
            <RunBadge run={run} ok="Up to date" failed={driveSyncOutcomeBadge[outcome].label} />
            {r !== undefined && r.error !== "" && <Text className={styles.muted}> {r.error}</Text>}
            {r?.failures.map((f) => (
              <Text key={f} block className={styles.muted}>
                {f}
              </Text>
            ))}
          </>
        );
      }}
    />
  );
}

const driveEventKinds = [
  EventKind.DRIVE_FILE_ARCHIVED,
  EventKind.DRIVE_FILE_CHANGED,
  EventKind.DRIVE_FILE_MOVED,
  EventKind.DRIVE_FILE_DELETED,
  EventKind.DRIVE_FILE_RESTORED,
  EventKind.DRIVE_FILE_DAMAGED,
  EventKind.DRIVE_FILE_REPAIRED,
];

/** A Drive file event as text; kept copies are named within the archive's Drive folder. */
function driveFileText(d: DriveFileEvent, driveDir: string | undefined): string {
  const kept =
    d.keptAs === ""
      ? ""
      : `; the previous copy is kept at ${driveDir === undefined ? d.keptAs : `${driveDir}/${d.keptAs}`}`;
  switch (d.change) {
    case DriveFileChange.ARCHIVED:
      return `${d.path} (${formatBytes(d.sizeBytes)})`;
    case DriveFileChange.CHANGED:
    case DriveFileChange.REPAIRED:
      return d.path + kept;
    case DriveFileChange.MOVED:
      return `${d.fromPath} → ${d.path}`;
    case DriveFileChange.DELETED:
      return `${d.path}; the archived copy is kept`;
    case DriveFileChange.RESTORED:
    case DriveFileChange.UNSPECIFIED:
      return d.path;
    case DriveFileChange.DAMAGED:
      return `${d.path}: ${d.reason}. It is downloaded again at the next iCloud Drive sync.`;
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
      ...driveEventKinds,
    ],
  },
  quarantined: { label: "Quarantined", kinds: [EventKind.QUARANTINED] },
  released: { label: "Released", kinds: [EventKind.QUARANTINE_RELEASED] },
  changed: { label: "Changed in iCloud", kinds: [EventKind.RENDITION_CHANGED] },
  moved: { label: "Moved", kinds: [EventKind.RENDITION_MOVED] },
  drive: { label: "Drive health", kinds: [EventKind.DRIVE_HEALTH_CHANGED] },
  iphone: { label: "iPhone backups", kinds: [EventKind.IPHONE_BACKUP_CHANGED] },
  icloudDrive: { label: "iCloud Drive", kinds: driveEventKinds },
  states: { label: "State changes", kinds: [EventKind.STATE_CHANGED] },
} as const;
type Filter = keyof typeof filters;

function isFilter(v: string | undefined): v is Filter {
  return v !== undefined && Object.hasOwn(filters, v);
}

function describe(e: Event, driveDir: string | undefined): { kind: string; color: BadgeColor; text: string } {
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
    case "driveHealthChanged": {
      const b = driveHealthBadge[e.detail.value.to];
      return { kind: "Drive health", color: b.color, text: [b.label, ...e.detail.value.reasons].join(": ") };
    }
    case "iphoneBackupChanged": {
      const c = e.detail.value;
      const b = iphoneStateBadge[c.to];
      return { kind: "iPhone backup", color: b.color, text: [`${c.deviceName}: ${b.label}`, ...c.reasons].join(": ") };
    }
    case "driveFile": {
      const b = driveFileChangeBadge[e.detail.value.change];
      return { kind: b.label, color: b.color, text: driveFileText(e.detail.value, driveDir) };
    }
    case undefined:
      return { kind: "Event", color: "informative", text: "" };
  }
}

function Events() {
  const styles = useStyles();
  const table = useTableStyles();
  const [filter, setFilter] = useState<Filter>("all");
  const settings = useQuery(AgentService.method.getSettings, {});
  const q = useInfiniteQuery(
    AgentService.method.listEvents,
    { kinds: [...filters[filter].kinds], pageToken: "" },
    { pageParamKey: "pageToken", getNextPageParam: nextPage },
  );
  const events = q.data?.pages.flatMap((p) => p.events) ?? [];
  const driveDir = settings.data?.settings?.driveDir;
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
        <Card className={table.tableBox}>
          <Table size="small" aria-label="Events" className={table.table}>
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
                const d = describe(e, driveDir);
                const at = toDate(e.at);
                return (
                  <TableRow key={e.id.toString()}>
                    <TableCell className={table.nowrap}>{at === undefined ? dash : formatWhen(at)}</TableCell>
                    <TableCell>
                      <Badge appearance="tint" color={d.color}>
                        {d.kind}
                      </Badge>
                    </TableCell>
                    <TableCell className={table.mono}>{e.asset?.filename ?? ""}</TableCell>
                    <TableCell className={table.detail}>{d.text}</TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
          {events.length === 0 && <Text className={table.muted}>Nothing recorded yet.</Text>}
        </Card>
      )}
      <MoreButton q={q} />
      <Text size={200} className={table.muted}>
        The event log is append-only: entries can&apos;t be edited or removed, including by homecloud itself.
      </Text>
    </>
  );
}
