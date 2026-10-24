import { useInfiniteQuery, useQuery } from "@connectrpc/connect-query";
import {
  Badge,
  Card,
  Dropdown,
  Field,
  makeStyles,
  Option,
  SearchBox,
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
import { keepPreviousData } from "@tanstack/react-query";
import { useState } from "react";

import { navigate, useLocation } from "../app/router";
import { searchDelayMs, useDebounced } from "../app/useDebounced";
import { LoadError } from "../components/LoadError";
import { Page } from "../components/Page";
import { MoreButton, RunBadge, RunTable } from "../components/RunTable";
import { nextPage, useTableStyles } from "../components/tables";
import {
  driveFileChangeBadge,
  healthBadge,
  driveSyncListed,
  driveSyncOutcomeBadge,
  iphoneCopyChangeBadge,
  iphoneStateBadge,
  repairStepBadge,
  stateLabel,
  tierLabel,
  type BadgeColor,
} from "../components/labels";
import { runRow } from "../components/runs";
import { formatBytes, formatCount, formatSpan, formatWhen, toDate } from "../format";
import {
  AgentService,
  DriveFileChange,
  DriveSyncOutcome,
  EventKind,
  IPhoneBackupCopyChange,
  PhotosActivity,
  QuarantineRepairStep,
  RunKind,
  type DriveFileEvent,
  type Event,
  type Run,
  type IPhoneBackupCopyEvent,
  type QuarantineRepairEvent,
} from "../gen/homecloud/agent/v3/agent_pb";
import { activityTabs, isActivityTab, type ActivityTab } from "./activityTabs";

const useStyles = makeStyles({
  filters: { display: "flex", flexWrap: "wrap", gap: tokens.spacingHorizontalM },
});

export function ActivityPage() {
  const loc = useLocation();
  const tabParam = loc.search.get("tab");
  const tab: ActivityTab = isActivityTab(tabParam) ? tabParam : "runs";

  const onTabSelect = (selectedTab: ActivityTab) => {
    navigate("/activity", {
      replace: true,
      search: selectedTab === "runs" ? undefined : { tab: selectedTab },
    });
  };

  return (
    <Page title="Activity">
      <TabList
        selectedValue={tab}
        onTabSelect={(_, d) => {
          if (isActivityTab(d.value)) {
            onTabSelect(d.value);
          }
        }}
      >
        {Object.entries(activityTabs).map(([value, label]) => (
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

/** The iPhone backup copy events, for the iPhone filter and "all". */
const iphoneEventKinds = [
  EventKind.IPHONE_BACKUP_CHANGED,
  EventKind.IPHONE_BACKUP_COPIED,
  EventKind.IPHONE_BACKUP_COPY_REMOVED,
  EventKind.IPHONE_BACKUP_COPY_DAMAGED,
  EventKind.IPHONE_BACKUP_PUSH_REFUSED,
];

const repairEventKinds = [
  EventKind.QUARANTINE_REPAIR_REQUESTED,
  EventKind.RENDITION_SET_ASIDE,
  EventKind.QUARANTINE_REPAIRED,
  EventKind.QUARANTINE_REPAIR_ABANDONED,
];

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

/** A repair step as text; the damaged copy is named within the archive. */
function repairText(r: QuarantineRepairEvent, root: string | undefined): string {
  const versions = r.versions.join(", ");
  switch (r.step) {
    case QuarantineRepairStep.REQUESTED:
      return `The failing ${versions} version will be downloaded again. The next sync moves the file aside first.`;
    case QuarantineRepairStep.SET_ASIDE:
      return r.keptAs === ""
        ? `The ${versions} version was already missing; kei downloads it again.`
        : `The ${versions} version was moved to ${root === undefined ? r.keptAs : `${root}/${r.keptAs}`}, and kei downloads it again.`;
    case QuarantineRepairStep.REPAIRED:
      return `Downloaded again (${versions}). It left quarantine, and every check runs again.`;
    case QuarantineRepairStep.ABANDONED:
      return `Not repaired: ${r.reason}. The file is back where it was, and it stays quarantined.`;
    case QuarantineRepairStep.UNSPECIFIED:
      return "";
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
      ...repairEventKinds,
      EventKind.DRIVE_HEALTH_CHANGED,
      EventKind.PHOTOS_ACTIVITY_CHANGED,
      ...iphoneEventKinds,
      ...driveEventKinds,
    ],
  },
  quarantined: { label: "Quarantined", kinds: [EventKind.QUARANTINED] },
  released: { label: "Released", kinds: [EventKind.QUARANTINE_RELEASED] },
  repairs: { label: "Repairs", kinds: repairEventKinds },
  changed: { label: "Changed in iCloud", kinds: [EventKind.RENDITION_CHANGED] },
  moved: { label: "Moved", kinds: [EventKind.RENDITION_MOVED] },
  drive: { label: "Drive health", kinds: [EventKind.DRIVE_HEALTH_CHANGED] },
  photos: { label: "iCloud Photos activity", kinds: [EventKind.PHOTOS_ACTIVITY_CHANGED] },
  iphone: { label: "iPhone backups", kinds: iphoneEventKinds },
  icloudDrive: { label: "iCloud Drive", kinds: driveEventKinds },
  states: { label: "State changes", kinds: [EventKind.STATE_CHANGED] },
} as const;
type Filter = keyof typeof filters;

function isFilter(v: string | undefined): v is Filter {
  return v !== undefined && Object.hasOwn(filters, v);
}

function iphoneCopyText(c: IPhoneBackupCopyEvent): string {
  const done = toDate(c.completedAt);
  const backup = `${c.deviceName === "" ? "The" : `${c.deviceName}: the`} backup of ${done === undefined ? "?" : formatWhen(done)}`;
  switch (c.change) {
    case IPhoneBackupCopyChange.COPIED:
      return `${backup} was copied into the archive (${formatCount(c.files)} files, ${formatBytes(c.copiedBytes)} written, the rest shared with the copy before)`;
    case IPhoneBackupCopyChange.REMOVED:
      return `${backup}: its copy was removed (${c.reason === "retention" ? "older than the copies kept" : "no longer on disk"})`;
    case IPhoneBackupCopyChange.DAMAGED:
      return c.files === 0n
        ? backup
        : `${backup}: ${formatCount(c.files)} files of its copy no longer match what was copied`;
    case IPhoneBackupCopyChange.PUSH_REFUSED:
      return `${backup}, pushed from another computer, was refused: ${c.reason}. The next push sends it again.`;
    case IPhoneBackupCopyChange.UNSPECIFIED:
      return backup;
  }
}

function describe(
  e: Event,
  dirs: { root: string | undefined; drive: string | undefined },
): { kind: string; color: BadgeColor; text: string } {
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
      const b = healthBadge[e.detail.value.to];
      return { kind: "Drive health", color: b.color, text: [b.label, ...e.detail.value.reasons].join(": ") };
    }
    case "photosActivityChanged": {
      const c = e.detail.value;
      const newest = toDate(c.newestAddedAt);
      const since = newest === undefined ? "?" : formatWhen(newest);
      return c.to === PhotosActivity.QUIET
        ? {
            kind: "iCloud Photos quiet",
            color: "warning",
            text: `Nothing was added to iCloud Photos since ${since}, longer than ${formatSpan(c.quietAfterSeconds)}. The phone may have stopped uploading.`,
          }
        : {
            kind: "iCloud Photos active",
            color: "success",
            text: `iCloud Photos receives new items; the newest was added ${since}.`,
          };
    }
    case "iphoneBackupChanged": {
      const c = e.detail.value;
      const b = iphoneStateBadge[c.to];
      return { kind: "iPhone backup", color: b.color, text: [`${c.deviceName}: ${b.label}`, ...c.reasons].join(": ") };
    }
    case "driveFile": {
      const b = driveFileChangeBadge[e.detail.value.change];
      return { kind: b.label, color: b.color, text: driveFileText(e.detail.value, dirs.drive) };
    }
    case "quarantineRepair": {
      const b = repairStepBadge[e.detail.value.step];
      return { kind: b.label, color: b.color, text: repairText(e.detail.value, dirs.root) };
    }
    case "iphoneBackupCopy": {
      const c = e.detail.value;
      if (c.change === IPhoneBackupCopyChange.DAMAGED && c.files === 0n) {
        return { kind: "Copy checked", color: "success", text: `${iphoneCopyText(c)}: every file matches again` };
      }
      const b = iphoneCopyChangeBadge[c.change];
      return { kind: b.label, color: b.color, text: iphoneCopyText(c) };
    }
    case undefined:
      return { kind: "Event", color: "informative", text: "" };
  }
}

function Events() {
  const styles = useStyles();
  const table = useTableStyles();
  const [filter, setFilter] = useState<Filter>("all");
  const [search, setSearch] = useState("");
  const query = useDebounced(search.trim(), searchDelayMs);
  const settings = useQuery(AgentService.method.getSettings, {});
  const q = useInfiniteQuery(
    AgentService.method.listEvents,
    { kinds: [...filters[filter].kinds], query, pageToken: "" },
    // The previous results stay while a new search loads.
    { pageParamKey: "pageToken", getNextPageParam: nextPage, placeholderData: keepPreviousData },
  );
  const events = q.data?.pages.flatMap((p) => p.events) ?? [];
  const dirs = { root: settings.data?.settings?.storageRoot, drive: settings.data?.settings?.driveDir };

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
        <Field label="Search" hint="A photo's file name, an iCloud Drive path or an iPhone's name">
          <SearchBox
            value={search}
            onChange={(_, d) => {
              setSearch(d.value);
            }}
          />
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
                <TableHeaderCell className={table.th}>When</TableHeaderCell>
                <TableHeaderCell className={table.th}>Event</TableHeaderCell>
                <TableHeaderCell className={table.th}>Item</TableHeaderCell>
                <TableHeaderCell className={table.th}>Detail</TableHeaderCell>
              </TableRow>
            </TableHeader>
            <TableBody>
              {events.map((e) => {
                const d = describe(e, dirs);
                const at = toDate(e.at);
                return (
                  <TableRow key={e.id.toString()}>
                    <TableCell className={table.nowrap}>{at === undefined ? dash : formatWhen(at)}</TableCell>
                    <TableCell className={table.nowrap}>
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
          {events.length === 0 && (
            <Text className={table.muted}>
              {query === "" ? "Nothing recorded yet." : "No event matches your search."}
            </Text>
          )}
        </Card>
      )}
      <MoreButton q={q} />
      <Text size={200} className={table.muted}>
        The event log is append-only: entries can&apos;t be edited or removed, including by homecloud itself.
      </Text>
    </>
  );
}
