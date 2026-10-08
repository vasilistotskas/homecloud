import { useInfiniteQuery, useQuery } from "@connectrpc/connect-query";
import {
  Badge,
  Button,
  Caption1,
  Card,
  Dialog,
  DialogBody,
  DialogContent,
  DialogSurface,
  DialogTitle,
  DialogTrigger,
  makeStyles,
  Menu,
  MenuGroup,
  MenuGroupHeader,
  MenuItemRadio,
  MenuList,
  MenuPopover,
  MenuTrigger,
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
import {
  ChevronLeftRegular,
  ChevronRightRegular,
  CopyRegular,
  DismissRegular,
  EyeOffRegular,
  FolderOpenRegular,
  ImageOffRegular,
  InfoRegular,
  LiveRegular,
  MaximizeRegular,
  MoreHorizontalRegular,
  VideoClipRegular,
} from "@fluentui/react-icons";
import { useEffect, useRef, useState } from "react";

import { useOverview } from "../api/overviewContext";
import { usePlatform } from "../app/platformContext";
import { LoadError } from "../components/LoadError";
import { Page } from "../components/Page";
import { mediaLabel } from "../components/labels";
import { useNotify } from "../components/toast";
import { useCopy } from "../components/useCopy";
import { formatBytes, formatCount, formatWhen, toDate } from "../format";
import { AgentService, AssetKind, AssetState, type LibraryItem } from "../gen/homecloud/agent/v3/agent_pb";

const useStyles = makeStyles({
  filtersRow: {
    display: "flex",
    alignItems: "center",
    justifyContent: "space-between",
    gap: tokens.spacingHorizontalS,
    flexWrap: "wrap",
  },
  filters: { display: "flex", flexWrap: "wrap", alignItems: "center", gap: tokens.spacingHorizontalS },
  viewOptions: {
    marginLeft: "auto",
    display: "flex",
    alignItems: "center",
    gap: tokens.spacingHorizontalXS,
  },
  actionsRow: { display: "flex", gap: tokens.spacingHorizontalS, flexWrap: "wrap" },
  split: {
    display: "flex",
    gap: tokens.spacingHorizontalL,
    alignItems: "flex-start",
    position: "relative",
    width: "100%",
  },
  grid: {
    flex: "1 1 0",
    minWidth: 0,
    display: "flex",
    flexDirection: "column",
    gap: tokens.spacingVerticalXL,
  },
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
  noImage: {
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    width: "100%",
    height: "100%",
    color: tokens.colorNeutralForeground3,
  },
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
  detail: {
    width: "320px",
    flexShrink: 0,
    position: "sticky",
    top: tokens.spacingVerticalL,
    maxHeight: "calc(100vh - 120px)",
    overflowY: "auto",
    gap: tokens.spacingVerticalM,
    boxSizing: "border-box",
    "@media (max-width: 767px)": {
      position: "fixed",
      top: "auto",
      bottom: 0,
      right: 0,
      left: 0,
      width: "auto",
      maxHeight: "70vh",
      zIndex: 100,
      boxShadow: tokens.shadow28,
      borderRadius: `${tokens.borderRadiusXLarge} ${tokens.borderRadiusXLarge} 0 0`,
    },
  },
  detailHeader: {
    display: "flex",
    alignItems: "flex-start",
    justifyContent: "space-between",
    gap: tokens.spacingHorizontalS,
  },
  detailTitle: {
    minWidth: 0,
    overflowWrap: "anywhere",
  },
  preview: {
    width: "100%",
    aspectRatio: "4 / 3",
    objectFit: "contain",
    borderRadius: tokens.borderRadiusMedium,
    backgroundColor: tokens.colorNeutralBackground3,
  },
  previewButton: {
    padding: 0,
    border: "none",
    background: "none",
    cursor: "pointer",
    borderRadius: tokens.borderRadiusMedium,
    position: "relative",
    display: "block",
    width: "100%",
    ":focus-visible": { outline: `3px solid ${tokens.colorStrokeFocus2}` },
  },
  previewOverlay: {
    position: "absolute",
    inset: 0,
    backgroundColor: "rgba(0, 0, 0, 0.4)",
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    color: "#ffffff",
    opacity: 0,
    transition: "opacity 0.15s ease-in-out",
    borderRadius: tokens.borderRadiusMedium,
    ":hover": { opacity: 1 },
  },
  lightboxSurface: {
    maxWidth: "94vw",
    width: "fit-content",
    maxHeight: "94vh",
    display: "flex",
    flexDirection: "column",
    padding: tokens.spacingHorizontalM,
  },
  lightboxBody: {
    display: "flex",
    flexDirection: "column",
    alignItems: "center",
    gap: tokens.spacingVerticalM,
    minHeight: 0,
  },
  lightboxContent: {
    display: "flex",
    flexDirection: "column",
    alignItems: "center",
    gap: tokens.spacingVerticalM,
    minHeight: 0,
  },
  lightboxImage: {
    maxWidth: "88vw",
    maxHeight: "72vh",
    objectFit: "contain",
    borderRadius: tokens.borderRadiusMedium,
    boxShadow: tokens.shadow16,
  },
  lightboxFooter: {
    display: "flex",
    justifyContent: "space-between",
    alignItems: "center",
    width: "100%",
    marginTop: tokens.spacingVerticalS,
  },
  lightboxNav: {
    display: "flex",
    gap: tokens.spacingHorizontalS,
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

const densities = [
  { id: "compact", label: "Compact", size: 84 },
  { id: "normal", label: "Normal", size: 120 },
  { id: "spacious", label: "Spacious", size: 200 },
] as const;
type GridDensity = (typeof densities)[number]["id"];

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
  const [selected, setSelected] = useState<string | undefined>(undefined);
  const q = useInfiniteQuery(
    AgentService.method.listAssets,
    { pageToken: "", kind, hidden },
    {
      pageParamKey: "pageToken",
      getNextPageParam: (last) => (last.nextPageToken === "" ? undefined : last.nextPageToken),
    },
  );
  const [lightboxOpen, setLightboxOpen] = useState(false);
  const [detailVisible, setDetailVisible] = useState(true);
  const [density, setDensity] = useState<GridDensity>(() => {
    const saved = localStorage.getItem("homecloud.gridDensity");
    return saved === "compact" || saved === "spacious" ? saved : "normal";
  });

  const onSelectDensity = (d: GridDensity) => {
    setDensity(d);
    localStorage.setItem("homecloud.gridDensity", d);
  };

  const currentSize = densities.find((d) => d.id === density)?.size ?? 120;
  const items = (q.data?.pages ?? []).flatMap((p) => p.items);
  const current = items.find((i) => itemKey(i) === selected) ?? items[0];
  const currentTaken = current === undefined ? undefined : toDate(current.createdAt);

  const currentIndex = current === undefined ? -1 : items.findIndex((i) => itemKey(i) === itemKey(current));
  const hasPrev = currentIndex > 0;
  const hasNext = currentIndex >= 0 && currentIndex < items.length - 1;

  const selectPrev = () => {
    const prev = items[currentIndex - 1];
    if (hasPrev && prev !== undefined) {
      setSelected(itemKey(prev));
    }
  };

  const selectNext = () => {
    const next = items[currentIndex + 1];
    if (hasNext && next !== undefined) {
      setSelected(itemKey(next));
    }
  };

  // Keyboard navigation for photo grid and lightbox
  useEffect(() => {
    function onKeyDown(e: KeyboardEvent) {
      if (
        e.target instanceof HTMLInputElement ||
        e.target instanceof HTMLTextAreaElement ||
        e.target instanceof HTMLSelectElement
      ) {
        return;
      }
      if (e.key === "ArrowLeft") {
        e.preventDefault();
        selectPrev();
      } else if (e.key === "ArrowRight") {
        e.preventDefault();
        selectNext();
      }
    }
    window.addEventListener("keydown", onKeyDown);
    return () => {
      window.removeEventListener("keydown", onKeyDown);
    };
  });

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
      <div className={styles.filtersRow}>
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
        <div className={styles.viewOptions}>
          <ToggleButton
            appearance="subtle"
            icon={<InfoRegular />}
            checked={detailVisible}
            onClick={() => {
              setDetailVisible((v) => !v);
            }}
            aria-label="Toggle details panel"
            title={detailVisible ? "Hide details panel" : "Show details panel"}
          />
          <Menu
            checkedValues={{ density: [density] }}
            onCheckedValueChange={(_, { name, checkedItems }) => {
              if (name === "density" && checkedItems[0]) {
                onSelectDensity(checkedItems[0] as GridDensity);
              }
            }}
          >
            <MenuTrigger disableButtonEnhancement>
              <Button
                appearance="subtle"
                icon={<MoreHorizontalRegular />}
                aria-label="Library view options"
                title="View options"
              />
            </MenuTrigger>
            <MenuPopover>
              <MenuList>
                <MenuGroup>
                  <MenuGroupHeader>Grid density</MenuGroupHeader>
                  <MenuItemRadio name="density" value="compact">
                    Compact
                  </MenuItemRadio>
                  <MenuItemRadio name="density" value="normal">
                    Normal
                  </MenuItemRadio>
                  <MenuItemRadio name="density" value="spacious">
                    Spacious
                  </MenuItemRadio>
                </MenuGroup>
              </MenuList>
            </MenuPopover>
          </Menu>
        </div>
      </div>
      {q.isPending && <Spinner label="Loading the library…" />}
      {q.isError && <LoadError what="the library" error={q.error} onRetry={() => void q.refetch()} />}
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
                <div
                  className={styles.tiles}
                  style={{ gridTemplateColumns: `repeat(auto-fill, minmax(${currentSize}px, 1fr))` }}
                >
                  {group.items.map((item) => (
                    <button
                      key={item.asset?.assetId}
                      type="button"
                      className={mergeClasses(styles.tile, item === current && styles.selected)}
                      aria-label={tileLabel(item)}
                      aria-pressed={item === current}
                      onClick={() => {
                        setSelected(itemKey(item));
                        setDetailVisible(true);
                      }}
                      onDoubleClick={() => {
                        setSelected(itemKey(item));
                        setLightboxOpen(true);
                      }}
                    >
                      {item.thumbnailUrl === "" ? (
                        <span className={styles.noImage}>
                          <ImageOffRegular fontSize={32} aria-hidden />
                        </span>
                      ) : (
                        <img className={styles.img} src={platform.mediaUrl(item.thumbnailUrl)} alt="" loading="lazy" />
                      )}
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
          {detailVisible && (
            <Detail
              item={current}
              onOpenLightbox={() => {
                setLightboxOpen(true);
              }}
              onClose={() => {
                setDetailVisible(false);
              }}
            />
          )}
        </div>
      )}
      {lightboxOpen && current !== undefined && (
        <Dialog
          open={lightboxOpen}
          onOpenChange={(_, data) => {
            setLightboxOpen(data.open);
          }}
        >
          <DialogSurface className={styles.lightboxSurface}>
            <DialogBody className={styles.lightboxBody}>
              <DialogTitle
                action={
                  <DialogTrigger action="close">
                    <Button appearance="subtle" aria-label="Close" icon={<DismissRegular />} />
                  </DialogTrigger>
                }
              >
                {current.asset?.filename ?? "Photo"}
              </DialogTitle>
              <DialogContent className={styles.lightboxContent}>
                {current.previewUrl !== "" ? (
                  <img
                    className={styles.lightboxImage}
                    src={platform.mediaUrl(current.previewUrl)}
                    alt={current.asset?.filename ?? ""}
                  />
                ) : (
                  <Text className={styles.muted}>No preview available.</Text>
                )}
                <div className={styles.lightboxFooter}>
                  <Caption1 className={styles.muted}>
                    {currentIndex + 1} of {items.length}
                    {currentTaken !== undefined && ` · ${formatWhen(currentTaken)}`}
                  </Caption1>
                  <div className={styles.lightboxNav}>
                    <Button
                      icon={<ChevronLeftRegular />}
                      disabled={!hasPrev}
                      onClick={selectPrev}
                      aria-label="Previous photo"
                    >
                      Previous
                    </Button>
                    <Button
                      icon={<ChevronRightRegular />}
                      disabled={!hasNext}
                      onClick={selectNext}
                      aria-label="Next photo"
                    >
                      Next
                    </Button>
                  </div>
                </div>
              </DialogContent>
            </DialogBody>
          </DialogSurface>
        </Dialog>
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

/** Identifies an item across refetches. */
function itemKey(item: LibraryItem): string {
  return `${item.asset?.library ?? ""}/${item.asset?.assetId ?? ""}`;
}

/** What a tile announces: its kind, file name and capture date. */
function tileLabel(item: LibraryItem): string {
  const taken = toDate(item.createdAt);
  return [kindLabel[item.kind], item.asset?.filename ?? "", taken === undefined ? "" : formatWhen(taken)]
    .filter((part) => part !== "")
    .join(", ");
}

const stateBadge: Record<AssetState, { label: string; color: "success" | "warning" | "danger" | "informative" }> = {
  [AssetState.UNSPECIFIED]: { label: "Unknown", color: "informative" },
  [AssetState.DISCOVERED]: { label: "Not downloaded yet", color: "warning" },
  [AssetState.DOWNLOADED]: { label: "Not verified yet", color: "warning" },
  [AssetState.VERIFIED]: { label: "Verified", color: "success" },
  [AssetState.QUARANTINED]: { label: "Quarantined", color: "danger" },
};

function Detail({
  item,
  onOpenLightbox,
  onClose,
}: {
  item: LibraryItem;
  onOpenLightbox: () => void;
  onClose?: () => void;
}) {
  const styles = useStyles();
  const platform = usePlatform();
  const notify = useNotify();
  const copy = useCopy();
  const ref = item.asset;
  const asset = useQuery(AgentService.method.getAsset, { library: ref?.library ?? "", assetId: ref?.assetId ?? "" });
  const taken = toDate(item.createdAt);
  const renditions = asset.data?.asset?.renditions ?? [];
  const display = renditions.find((r) => r.version === item.displayVersion);
  return (
    <Card className={styles.detail} aria-label="Details">
      <div className={styles.detailHeader}>
        <div className={styles.detailTitle}>
          <Subtitle1 as="h2">{ref?.filename}</Subtitle1>
          <Caption1 block className={styles.muted}>
            {kindLabel[item.kind]}
            {taken !== undefined && ` · ${formatWhen(taken)}`}
          </Caption1>
        </div>
        {onClose !== undefined && (
          <Button
            appearance="subtle"
            size="small"
            icon={<DismissRegular />}
            aria-label="Close details"
            onClick={onClose}
          />
        )}
      </div>
      {item.previewUrl === "" ? (
        <Text className={styles.muted}>No picture yet: this item&apos;s photo hasn&apos;t been downloaded.</Text>
      ) : (
        <button type="button" className={styles.previewButton} onClick={onOpenLightbox} aria-label="Enlarge photo">
          <img className={styles.preview} src={platform.mediaUrl(item.previewUrl)} alt={ref?.filename ?? ""} />
          <span className={styles.previewOverlay}>
            <MaximizeRegular fontSize={24} />
          </span>
        </button>
      )}
      <div>
        <Badge appearance="tint" color={stateBadge[item.state].color}>
          {stateBadge[item.state].label}
        </Badge>
      </div>
      {asset.isPending && <Spinner size="tiny" />}
      {asset.isError && <LoadError what="this item's files" error={asset.error} onRetry={() => void asset.refetch()} />}
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
      {display !== undefined && (
        <div className={styles.actionsRow}>
          {platform.kind === "desktop" && platform.connection.kind === "this_pc" && (
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
          <Button
            icon={<CopyRegular />}
            onClick={() => {
              copy(display.localPath, "Path");
            }}
          >
            Copy path
          </Button>
        </div>
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
