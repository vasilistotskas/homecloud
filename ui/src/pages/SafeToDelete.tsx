import { useInfiniteQuery, useQuery } from "@connectrpc/connect-query";
import {
  Button,
  Caption1,
  Card,
  makeStyles,
  MessageBar,
  MessageBarBody,
  Spinner,
  Subtitle2,
  Text,
  tokens,
} from "@fluentui/react-components";
import { CopyRegular, FolderOpenRegular, ImageOffRegular } from "@fluentui/react-icons";
import { useEffect, useRef } from "react";

import { usePlatform } from "../app/platformContext";
import { viewerTimeZone } from "../calendar";
import { LoadError } from "../components/LoadError";
import { Page } from "../components/Page";
import { useNotify } from "../components/toast";
import { useCopy } from "../components/useCopy";
import { formatBytes, formatCount, formatSpan, toDate } from "../format";
import {
  AgentService,
  type SafeToDelete,
  type SafeToDeleteItem,
  type SafeToDeleteMonth,
} from "../gen/homecloud/agent/v3/agent_pb";

const useStyles = makeStyles({
  intro: { color: tokens.colorNeutralForeground3, maxWidth: "720px" },
  muted: { color: tokens.colorNeutralForeground3 },
  card: { gap: tokens.spacingVerticalXS, maxWidth: "720px" },
  export: { display: "flex", flexWrap: "wrap", alignItems: "center", gap: tokens.spacingHorizontalS },
  mono: { fontFamily: tokens.fontFamilyMonospace, fontSize: tokens.fontSizeBase200, overflowWrap: "anywhere" },
  months: { display: "flex", flexDirection: "column", gap: tokens.spacingVerticalXL },
  month: { display: "flex", flexDirection: "column", gap: tokens.spacingVerticalS },
  monthHead: { display: "flex", flexWrap: "wrap", alignItems: "baseline", gap: tokens.spacingHorizontalM },
  rows: { display: "flex", flexDirection: "column", gap: tokens.spacingVerticalXS, margin: 0, padding: 0 },
  row: {
    display: "grid",
    gridTemplateColumns: "48px 1fr auto",
    alignItems: "center",
    gap: tokens.spacingHorizontalM,
    listStyleType: "none",
  },
  thumb: {
    width: "48px",
    height: "48px",
    borderRadius: tokens.borderRadiusMedium,
    objectFit: "cover",
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    backgroundColor: tokens.colorNeutralBackground5,
    color: tokens.colorNeutralForeground3,
  },
  name: { display: "flex", flexDirection: "column", minWidth: 0 },
  filename: { overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" },
  sentinel: { height: "1px" },
});

const monthFormat = new Intl.DateTimeFormat(undefined, { month: "long", year: "numeric" });
const dayFormat = new Intl.DateTimeFormat(undefined, { dateStyle: "medium" });

/** "1 item", "1,204 items". */
function countItems(n: bigint): string {
  return `${formatCount(n)} ${n === 1n ? "item" : "items"}`;
}

/** A calendar month as the report keys its totals; year and month are 0 when the capture date is unknown. */
function monthKey(year: number, month: number): string {
  return `${year}-${month}`;
}

interface MonthGroup {
  year: number;
  month: number;
  items: SafeToDeleteItem[];
}

/** Groups consecutive items (newest first) by the month they were taken. */
function byMonth(items: SafeToDeleteItem[]): MonthGroup[] {
  const groups: MonthGroup[] = [];
  for (const item of items) {
    const taken = toDate(item.item?.createdAt);
    const year = taken?.getFullYear() ?? 0;
    const month = taken === undefined ? 0 : taken.getMonth() + 1;
    const last = groups.at(-1);
    if (last?.year === year && last.month === month) {
      last.items.push(item);
    } else {
      groups.push({ year, month, items: [item] });
    }
  }
  return groups;
}

export function SafeToDeletePage() {
  const styles = useStyles();
  const report = useQuery(AgentService.method.getSafeToDelete, { timeZone: viewerTimeZone });
  const r = report.data?.report;
  return (
    <Page
      title="Safe to delete"
      actions={
        r?.withheld === "" && (
          <Text className={styles.muted}>
            {countItems(r.assets)} · {formatBytes(r.bytes)}
          </Text>
        )
      }
    >
      <Text className={styles.intro}>
        These photos and videos are safe in your archive, so you can delete them from iCloud Photos to free space: every
        file was verified and later checked again on disk, they were added to iCloud
        {r === undefined ? " long enough ago" : ` more than ${formatSpan(r.minAgeSeconds)} ago`}, none is in quarantine,
        and the archive drive is healthy. homecloud deletes nothing; delete them in Apple&apos;s Photos app, which keeps
        them in Recently Deleted for 30 days.
      </Text>
      {report.isPending && <Spinner label="Checking the archive…" />}
      {report.isError && (
        <LoadError what="the safe-to-delete list" error={report.error} onRetry={() => void report.refetch()} />
      )}
      {r !== undefined && <Report report={r} />}
    </Page>
  );
}

function Report({ report: r }: { report: SafeToDelete }) {
  const styles = useStyles();
  const waiting = [
    {
      n: r.awaitingScrub,
      text: "not checked again on disk yet (the scrub re-checks a share of the archive on its schedule)",
    },
    { n: r.tooRecent, text: `added to iCloud less than ${formatSpan(r.minAgeSeconds)} ago` },
    { n: r.notVerified, text: "not verified yet, in quarantine or being repaired" },
  ].filter((w) => w.n > 0n);
  return (
    <>
      {r.withheld !== "" && (
        <MessageBar intent="warning">
          <MessageBarBody>
            Nothing is listed: {r.withheld}. The list comes back once the drive is healthy again.
          </MessageBarBody>
        </MessageBar>
      )}
      {r.withheld === "" && r.assets === 0n && (
        <MessageBar intent="info">
          <MessageBarBody>Nothing is safe to delete yet.</MessageBarBody>
        </MessageBar>
      )}
      {waiting.length > 0 && (
        <Card className={styles.card}>
          <Subtitle2 as="h2">Not listed yet</Subtitle2>
          {waiting.map((w) => (
            <Text key={w.text}>
              {formatCount(w.n)} {w.text}
            </Text>
          ))}
        </Card>
      )}
      <ExportPath path={r.exportPath} />
      {r.withheld === "" && r.assets > 0n && <Months months={r.months} />}
    </>
  );
}

function ExportPath({ path }: { path: string }) {
  const styles = useStyles();
  const platform = usePlatform();
  const notify = useNotify();
  const copy = useCopy();
  return (
    <div className={styles.export}>
      <Caption1 className={styles.muted}>Saved after every sync as</Caption1>
      <Caption1 className={styles.mono}>{path}</Caption1>
      {platform.kind === "desktop" && platform.connection.kind === "this_pc" && (
        <Button
          size="small"
          appearance="subtle"
          icon={<FolderOpenRegular />}
          onClick={() => {
            platform.revealInFolder(path).catch((err: unknown) => {
              notify("error", "Couldn't show the file", String(err));
            });
          }}
        >
          Show in folder
        </Button>
      )}
      <Button
        size="small"
        appearance="subtle"
        icon={<CopyRegular />}
        onClick={() => {
          copy(path, "Path");
        }}
      >
        Copy path
      </Button>
    </div>
  );
}

function Months({ months }: { months: SafeToDeleteMonth[] }) {
  const styles = useStyles();
  const platform = usePlatform();
  const q = useInfiniteQuery(
    AgentService.method.listSafeToDelete,
    { pageToken: "" },
    {
      pageParamKey: "pageToken",
      getNextPageParam: (last) => (last.nextPageToken === "" ? undefined : last.nextPageToken),
    },
  );
  const { hasNextPage, isFetchingNextPage, fetchNextPage } = q;

  // Load the next page when the end of the list scrolls into view.
  const sentinel = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const el = sentinel.current;
    if (el === null) {
      return;
    }
    const observer = new IntersectionObserver((entries) => {
      if (entries.some((e) => e.isIntersecting) && hasNextPage && !isFetchingNextPage) {
        void fetchNextPage();
      }
    });
    observer.observe(el);
    return () => {
      observer.disconnect();
    };
  }, [hasNextPage, isFetchingNextPage, fetchNextPage]);

  if (q.isPending) {
    return <Spinner label="Loading the list…" />;
  }
  if (q.isError) {
    return <LoadError what="the safe-to-delete list" error={q.error} onRetry={() => void q.refetch()} />;
  }
  const totals = new Map(months.map((m) => [monthKey(m.year, m.month), m]));
  const items = q.data.pages.flatMap((p) => p.items);
  return (
    <div className={styles.months}>
      {byMonth(items).map((group) => {
        const key = monthKey(group.year, group.month);
        const total = totals.get(key);
        const title = group.year === 0 ? "Date unknown" : monthFormat.format(new Date(group.year, group.month - 1));
        return (
          <section key={key} className={styles.month} aria-label={title}>
            <div className={styles.monthHead}>
              <Subtitle2 as="h2">{title}</Subtitle2>
              {total !== undefined && (
                <Caption1 className={styles.muted}>
                  {countItems(total.assets)} · {formatBytes(total.bytes)}
                </Caption1>
              )}
            </div>
            <ul className={styles.rows}>
              {group.items.map((it) => {
                const taken = toDate(it.item?.createdAt);
                const thumb = it.item?.thumbnailUrl ?? "";
                return (
                  <li
                    key={`${it.item?.asset?.library}/${it.item?.asset?.assetId}`}
                    className={styles.row}
                    title={it.path}
                  >
                    {thumb === "" ? (
                      <span className={styles.thumb}>
                        <ImageOffRegular aria-hidden />
                      </span>
                    ) : (
                      <img className={styles.thumb} src={platform.mediaUrl(thumb)} alt="" loading="lazy" />
                    )}
                    <span className={styles.name}>
                      <Text weight="semibold" className={styles.filename}>
                        {it.item?.asset?.filename}
                      </Text>
                      <Caption1 className={styles.muted}>
                        {taken === undefined ? "Date unknown" : `Taken ${dayFormat.format(taken)}`}
                      </Caption1>
                    </span>
                    <Caption1>{formatBytes(it.sizeBytes)}</Caption1>
                  </li>
                );
              })}
            </ul>
          </section>
        );
      })}
      <div ref={sentinel} className={styles.sentinel} />
      {isFetchingNextPage && <Spinner size="tiny" />}
    </div>
  );
}
