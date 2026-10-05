import { useInfiniteQuery, useQuery } from "@connectrpc/connect-query";
import {
  Badge,
  Button,
  Caption1,
  Card,
  makeStyles,
  mergeClasses,
  MessageBar,
  MessageBarBody,
  Spinner,
  Subtitle1,
  Subtitle2,
  Text,
  ToggleButton,
  tokens,
} from "@fluentui/react-components";
import { EyeOffRegular, FolderOpenRegular, LiveRegular, VideoClipRegular } from "@fluentui/react-icons";
import { useEffect, useRef, useState } from "react";

import { useOverview } from "../api/overviewContext";
import { usePlatform } from "../app/platformContext";
import { Page } from "../components/Page";
import { mediaLabel } from "../components/labels";
import { useNotify } from "../components/toast";
import { formatBytes, formatCount, formatWhen, toDate } from "../format";
import { AgentService, AssetKind, AssetState, type LibraryItem } from "../gen/homecloud/agent/v1/agent_pb";

const pageSize = 60;

const useStyles = makeStyles({
  filters: { display: "flex", flexWrap: "wrap", alignItems: "center", gap: tokens.spacingHorizontalS },
  split: { display: "flex", flexWrap: "wrap", gap: tokens.spacingHorizontalL, alignItems: "flex-start" },
  grid: { flex: "999 1 520px", minWidth: 0, display: "flex", flexDirection: "column", gap: tokens.spacingVerticalXL },
  month: { display: "flex", flexDirection: "column", gap: tokens.spacingVerticalS },
  tiles: {
    display: "grid",
    gridTemplateColumns: "repeat(auto-fill, minmax(112px, 1fr))",
    gap: tokens.spacingHorizontalXS,
  },
  tile: {
    position: "relative",
    aspectRatio: "1",
    padding: 0,
    border: "none",
    borderRadius: tokens.borderRadiusMedium,
    overflow: "hidden",
    cursor: "pointer",
    backgroundColor: tokens.colorNeutralBackground5,
    ":focus-visible": { outline: `3px solid ${tokens.colorStrokeFocus2}` },
  },
  selected: { outline: `3px solid ${tokens.colorBrandStroke1}`, outlineOffset: "2px" },
  img: { width: "100%", height: "100%", objectFit: "cover", display: "block" },
  tag: {
    position: "absolute",
    right: "6px",
    bottom: "6px",
    display: "flex",
    alignItems: "center",
    padding: "1px 6px",
    borderRadius: tokens.borderRadiusCircular,
    backgroundColor: "rgba(0, 0, 0, 0.55)",
    color: "#ffffff",
    fontSize: tokens.fontSizeBase100,
  },
  detail: { flex: "1 1 300px", gap: tokens.spacingVerticalM, position: "sticky", top: tokens.spacingVerticalL },
  preview: {
    width: "100%",
    aspectRatio: "4 / 3",
    objectFit: "contain",
    borderRadius: tokens.borderRadiusMedium,
    backgroundColor: tokens.colorNeutralBackground3,
  },
  facts: {
    display: "grid",
    gridTemplateColumns: "auto 1fr",
    gap: `${tokens.spacingVerticalXS} ${tokens.spacingHorizontalL}`,
  },
  muted: { color: tokens.colorNeutralForeground3 },
  path: { fontFamily: tokens.fontFamilyMonospace, fontSize: tokens.fontSizeBase200, overflowWrap: "anywhere" },
  sentinel: { height: "1px" },
});

const kinds = [
  { kind: AssetKind.UNSPECIFIED, label: "All" },
  { kind: AssetKind.PHOTO, label: "Photos" },
  { kind: AssetKind.VIDEO, label: "Videos" },
  { kind: AssetKind.LIVE_PHOTO, label: "Live Photos" },
] as const;

const monthFormat = new Intl.DateTimeFormat(undefined, { month: "long", year: "numeric" });

/** Groups consecutive items (newest first) by the month they were taken. */
function byMonth(items: LibraryItem[]): { title: string; items: LibraryItem[] }[] {
  const groups: { title: string; items: LibraryItem[] }[] = [];
  for (const item of items) {
    const taken = toDate(item.createdAt);
    const title = taken === undefined ? "Date unknown" : monthFormat.format(taken);
    const last = groups.at(-1);
    if (last?.title === title) {
      last.items.push(item);
    } else {
      groups.push({ title, items: [item] });
    }
  }
  return groups;
}

export function LibraryPage() {
  const styles = useStyles();
  const platform = usePlatform();
  const { overview } = useOverview();
  const [kind, setKind] = useState<AssetKind>(AssetKind.UNSPECIFIED);
  const [hidden, setHidden] = useState(false);
  const [selected, setSelected] = useState<LibraryItem | undefined>(undefined);
  const q = useInfiniteQuery(
    AgentService.method.listAssets,
    { pageSize, pageToken: "", kind, hidden },
    {
      pageParamKey: "pageToken",
      getNextPageParam: (last) => (last.nextPageToken === "" ? undefined : last.nextPageToken),
    },
  );
  const items = (q.data?.pages ?? []).flatMap((p) => p.items);
  const current = selected ?? items[0];

  // Load the next page when the end of the grid scrolls into view.
  const sentinel = useRef<HTMLDivElement>(null);
  const { hasNextPage, isFetchingNextPage, fetchNextPage } = q;
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

  const total = overview?.archive?.files;
  return (
    <Page
      title="Library"
      actions={
        total !== undefined && (
          <Text className={styles.muted}>
            {formatCount(total)} files · {formatBytes(overview?.archive?.bytes ?? 0n)}
          </Text>
        )
      }
    >
      <div className={styles.filters} role="group" aria-label="Show">
        {kinds.map((k) => (
          <ToggleButton
            key={k.kind}
            checked={kind === k.kind}
            onClick={() => {
              setKind(k.kind);
              setSelected(undefined);
            }}
          >
            {k.label}
          </ToggleButton>
        ))}
        <ToggleButton
          icon={<EyeOffRegular />}
          checked={hidden}
          onClick={() => {
            setHidden((h) => !h);
            setSelected(undefined);
          }}
        >
          Hidden
        </ToggleButton>
      </div>
      {q.isPending && <Spinner label="Loading the library…" />}
      {q.isError && (
        <MessageBar intent="error">
          <MessageBarBody>Couldn&apos;t load the library: {q.error.message}</MessageBarBody>
        </MessageBar>
      )}
      {q.isSuccess && items.length === 0 && (
        <MessageBar intent="info">
          <MessageBarBody>
            {hidden ? "No hidden photos are archived." : "Nothing here yet. New items appear after the next sync."}
          </MessageBarBody>
        </MessageBar>
      )}
      {current !== undefined && (
        <div className={styles.split}>
          <div className={styles.grid}>
            {byMonth(items).map((group) => (
              <section key={group.title} className={styles.month} aria-label={group.title}>
                <Subtitle2 as="h2">{group.title}</Subtitle2>
                <div className={styles.tiles}>
                  {group.items.map((item) => (
                    <button
                      key={item.asset?.assetId}
                      type="button"
                      className={mergeClasses(styles.tile, item === current && styles.selected)}
                      aria-label={item.asset?.filename}
                      aria-pressed={item === current}
                      onClick={() => {
                        setSelected(item);
                      }}
                    >
                      <img className={styles.img} src={platform.mediaUrl(item.thumbnailUrl)} alt="" loading="lazy" />
                      {item.kind === AssetKind.VIDEO && (
                        <span className={styles.tag}>
                          <VideoClipRegular aria-label="Video" />
                        </span>
                      )}
                      {item.kind === AssetKind.LIVE_PHOTO && (
                        <span className={styles.tag}>
                          <LiveRegular aria-label="Live Photo" />
                        </span>
                      )}
                    </button>
                  ))}
                </div>
              </section>
            ))}
            <div ref={sentinel} className={styles.sentinel} />
            {isFetchingNextPage && <Spinner size="tiny" />}
          </div>
          <Detail item={current} />
        </div>
      )}
    </Page>
  );
}

const kindLabel: Record<AssetKind, string> = {
  [AssetKind.UNSPECIFIED]: "Item",
  [AssetKind.PHOTO]: "Photo",
  [AssetKind.VIDEO]: "Video",
  [AssetKind.LIVE_PHOTO]: "Live Photo",
};

function Detail({ item }: { item: LibraryItem }) {
  const styles = useStyles();
  const platform = usePlatform();
  const notify = useNotify();
  const ref = item.asset;
  const asset = useQuery(AgentService.method.getAsset, { library: ref?.library ?? "", assetId: ref?.assetId ?? "" });
  const taken = toDate(item.createdAt);
  const renditions = asset.data?.asset?.renditions ?? [];
  const display = renditions.find((r) => r.version === item.displayVersion);
  return (
    <Card className={styles.detail} aria-label="Details">
      <img className={styles.preview} src={platform.mediaUrl(item.previewUrl)} alt={ref?.filename ?? ""} />
      <div>
        <Subtitle1 as="h2">{ref?.filename}</Subtitle1>
        <Caption1 block className={styles.muted}>
          {kindLabel[item.kind]}
          {taken !== undefined && ` · ${formatWhen(taken)}`}
        </Caption1>
      </div>
      <div>
        {item.state === AssetState.VERIFIED ? (
          <Badge appearance="tint" color="success">
            Verified
          </Badge>
        ) : (
          <Badge appearance="tint" color="warning">
            Not verified yet
          </Badge>
        )}
      </div>
      {asset.isPending && <Spinner size="tiny" />}
      {renditions.length > 0 && (
        <div className={styles.facts}>
          {renditions.map((r) => (
            <FactRow
              key={r.version}
              label={r.version}
              value={`${mediaLabel[r.mediaType]} · ${formatBytes(r.sizeBytes)}`}
            />
          ))}
        </div>
      )}
      {display !== undefined && <Text className={styles.path}>{display.localPath}</Text>}
      {platform.kind === "desktop" && display !== undefined && (
        <Button
          icon={<FolderOpenRegular />}
          onClick={() => {
            platform.revealInFolder(display.localPath).catch((err: unknown) => {
              notify("error", "Couldn't show the file", String(err));
            });
          }}
        >
          Show in folder
        </Button>
      )}
    </Card>
  );
}

function FactRow({ label, value }: { label: string; value: string }) {
  const styles = useStyles();
  return (
    <>
      <Text className={styles.muted}>{label}</Text>
      <Text>{value}</Text>
    </>
  );
}
