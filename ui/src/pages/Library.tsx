import { timestampFromDate } from "@bufbuild/protobuf/wkt";
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
  Dropdown,
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
  Option,
  Spinner,
  Subtitle1,
  Subtitle2,
  Text,
  ToggleButton,
  tokens,
  useArrowNavigationGroup,
} from "@fluentui/react-components";
import {
  ChevronLeftRegular,
  ChevronRightRegular,
  CopyRegular,
  DismissRegular,
  EyeOffRegular,
  FolderOpenRegular,
  HistoryRegular,
  ImageOffRegular,
  InfoRegular,
  LiveRegular,
  MaximizeRegular,
  MoreHorizontalRegular,
  VideoClipRegular,
} from "@fluentui/react-icons";
import { useEffect, useRef, useState, type KeyboardEvent } from "react";

import { useOverview } from "../api/overviewContext";
import { narrowWindow } from "../app/breakpoints";
import { usePlatform } from "../app/platformContext";
import { navigate, useLocation } from "../app/router";
import { readStored, storageKeys, writeStored } from "../app/storage";
import { endOfMonth, monthParam, parseMonthParam, today, viewerTimeZone, type Day } from "../calendar";
import { LoadError } from "../components/LoadError";
import { Page } from "../components/Page";
import { mediaLabel } from "../components/labels";
import { useNotify } from "../components/toast";
import { useCopy } from "../components/useCopy";
import { formatBytes, formatCount, formatWhen, toDate } from "../format";
import { AgentService, AssetKind, AssetState, type LibraryItem } from "../gen/homecloud/agent/v3/agent_pb";
import { onThisDayView } from "./libraryView";

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
  tiles: { display: "grid", gap: tokens.spacingHorizontalXS },
  compact: { gridTemplateColumns: "repeat(auto-fill, minmax(84px, 1fr))" },
  normal: { gridTemplateColumns: "repeat(auto-fill, minmax(112px, 1fr))" },
  spacious: { gridTemplateColumns: "repeat(auto-fill, minmax(200px, 1fr))" },
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
    [narrowWindow]: {
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
  // Until the user picks an item, a narrow window shows no sheet over the grid.
  detailUnpicked: { [narrowWindow]: { display: "none" } },
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
    backgroundColor: tokens.colorBackgroundOverlay,
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    color: tokens.colorNeutralForegroundStaticInverted,
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
const dayOfYearFormat = new Intl.DateTimeFormat(undefined, { month: "long", day: "numeric" });

const densities = { compact: "Compact", normal: "Normal", spacious: "Spacious" } as const;
type GridDensity = keyof typeof densities;

function isDensity(v: unknown): v is GridDensity {
  return typeof v === "string" && Object.hasOwn(densities, v);
}

/** Groups consecutive items (newest first) by the month, or the year, they were taken. */
function byPeriod(items: LibraryItem[], period: "month" | "year"): { title: string; items: LibraryItem[] }[] {
  const groups: { title: string; items: LibraryItem[] }[] = [];
  for (const item of items) {
    const taken = toDate(item.createdAt);
    const title =
      taken === undefined
        ? "Date unknown"
        : period === "month"
          ? monthFormat.format(taken)
          : String(taken.getFullYear());
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
  // The view and the month to start at live in the address, so a link can open them.
  const loc = useLocation();
  const onThisDay = loc.search.get("view") === onThisDayView;
  const month = onThisDay ? undefined : parseMonthParam(loc.search.get("month"));
  const day: Day = today();
  const showView = (search: Record<string, string> | undefined) => {
    navigate("/library", { replace: true, search });
    setSelected(undefined);
  };
  const months = useQuery(
    AgentService.method.listCaptureMonths,
    { kind, hidden, timeZone: viewerTimeZone },
    { enabled: !onThisDay },
  );
  const q = useInfiniteQuery(
    AgentService.method.listAssets,
    {
      pageToken: "",
      kind,
      hidden,
      ...(onThisDay && { onThisDay: day, timeZone: viewerTimeZone }),
      ...(month !== undefined && { takenBefore: timestampFromDate(endOfMonth(month.year, month.month)) }),
    },
    {
      pageParamKey: "pageToken",
      getNextPageParam: (last) => (last.nextPageToken === "" ? undefined : last.nextPageToken),
    },
  );
  const [lightboxOpen, setLightboxOpen] = useState(false);
  const [detailVisible, setDetailVisible] = useState(true);
  const [density, setDensity] = useState<GridDensity>(() => readStored(storageKeys.gridDensity, isDensity) ?? "normal");
  // Arrow keys move between tiles (Tab leaves the grid); the focused tile is the selected one.
  const arrowKeys = useArrowNavigationGroup({ axis: "grid-linear", memorizeCurrent: true });

  const { hasNextPage, isFetchingNextPage, fetchNextPage } = q;
  const items = (q.data?.pages ?? []).flatMap((p) => p.items);
  const current = items.find((i) => itemKey(i) === selected) ?? items[0];
  const index = current === undefined ? -1 : items.indexOf(current);
  const prev = items[index - 1];
  const next = items[index + 1];
  // The lightbox steps past the loaded items by loading the next page.
  const showNext =
    next !== undefined
      ? () => {
          setSelected(itemKey(next));
        }
      : hasNextPage
        ? () => {
            void fetchNextPage().then((r) => {
              const loaded = (r.data?.pages ?? []).flatMap((p) => p.items)[index + 1];
              if (loaded !== undefined) {
                setSelected(itemKey(loaded));
              }
            });
          }
        : undefined;

  // Load the next page when the end of the grid scrolls into view.
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
          <ToggleButton
            icon={<HistoryRegular />}
            checked={onThisDay}
            onClick={() => {
              showView(onThisDay ? undefined : { view: onThisDayView });
            }}
          >
            On this day
          </ToggleButton>
          <Dropdown
            aria-label="Go to month"
            disabled={onThisDay || !months.isSuccess}
            value={month === undefined ? "Newest" : monthFormat.format(new Date(month.year, month.month - 1))}
            selectedOptions={[month === undefined ? "" : monthParam(month.year, month.month)]}
            onOptionSelect={(_, data) => {
              showView(
                data.optionValue === undefined || data.optionValue === "" ? undefined : { month: data.optionValue },
              );
            }}
          >
            <Option value="">Newest</Option>
            {(months.data?.months ?? []).map((m) => {
              const label = monthFormat.format(new Date(m.year, m.month - 1));
              return (
                <Option key={monthParam(m.year, m.month)} value={monthParam(m.year, m.month)} text={label}>
                  {label} · {formatCount(m.assets)}
                </Option>
              );
            })}
          </Dropdown>
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
              const d = checkedItems[0];
              if (name === "density" && isDensity(d)) {
                setDensity(d);
                writeStored(storageKeys.gridDensity, d);
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
                  {Object.entries(densities).map(([value, label]) => (
                    <MenuItemRadio key={value} name="density" value={value}>
                      {label}
                    </MenuItemRadio>
                  ))}
                </MenuGroup>
              </MenuList>
            </MenuPopover>
          </Menu>
        </div>
      </div>
      {q.isPending && <Spinner label="Loading the library…" />}
      {q.isError && <LoadError what="the library" error={q.error} onRetry={() => void q.refetch()} />}
      {months.isError && <LoadError what="the months" error={months.error} onRetry={() => void months.refetch()} />}
      {q.isSuccess && items.length === 0 && (
        <MessageBar intent="info">
          <MessageBarBody>
            {onThisDay
              ? `Nothing was taken on ${dayOfYearFormat.format(new Date(day.year, day.month - 1, day.day))} in earlier years.`
              : month !== undefined
                ? `Nothing was taken in or before ${monthFormat.format(new Date(month.year, month.month - 1))}.`
                : hidden
                  ? "No hidden photos are archived."
                  : "Nothing here yet. New items appear after the next sync."}
          </MessageBarBody>
        </MessageBar>
      )}
      {current !== undefined && (
        <div className={styles.split}>
          <div className={styles.grid} {...arrowKeys}>
            {byPeriod(items, onThisDay ? "year" : "month").map((group) => (
              <section key={group.title} className={styles.month} aria-label={group.title}>
                <Subtitle2 as="h2">{group.title}</Subtitle2>
                <div className={mergeClasses(styles.tiles, styles[density])}>
                  {group.items.map((item) => (
                    <button
                      key={item.asset?.assetId}
                      type="button"
                      className={mergeClasses(styles.tile, item === current && styles.selected)}
                      aria-label={tileLabel(item)}
                      aria-pressed={item === current}
                      onFocus={() => {
                        setSelected(itemKey(item));
                      }}
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
              picked={selected !== undefined}
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
        <Lightbox
          item={current}
          onPrev={
            prev === undefined
              ? undefined
              : () => {
                  setSelected(itemKey(prev));
                }
          }
          onNext={showNext}
          onClose={() => {
            setLightboxOpen(false);
          }}
        />
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

/** A picture at the size of the window; arrow keys step through the library. */
function Lightbox({
  item,
  onPrev,
  onNext,
  onClose,
}: {
  item: LibraryItem;
  onPrev: (() => void) | undefined;
  onNext: (() => void) | undefined;
  onClose: () => void;
}) {
  const styles = useStyles();
  const platform = usePlatform();
  const taken = toDate(item.createdAt);
  const onKeyDown = (e: KeyboardEvent) => {
    const step = { ArrowLeft: onPrev, ArrowRight: onNext }[e.key];
    if (step !== undefined) {
      e.preventDefault();
      step();
    }
  };
  return (
    <Dialog
      open
      onOpenChange={(_, data) => {
        if (!data.open) {
          onClose();
        }
      }}
    >
      <DialogSurface className={styles.lightboxSurface} onKeyDown={onKeyDown}>
        <DialogBody className={styles.lightboxBody}>
          <DialogTitle
            action={
              <DialogTrigger action="close">
                <Button appearance="subtle" aria-label="Close" icon={<DismissRegular />} />
              </DialogTrigger>
            }
          >
            {item.asset?.filename}
          </DialogTitle>
          <DialogContent className={styles.lightboxContent}>
            {item.previewUrl === "" ? (
              <Text className={styles.muted}>No picture yet: this item&apos;s photo hasn&apos;t been downloaded.</Text>
            ) : (
              <img
                className={styles.lightboxImage}
                src={platform.mediaUrl(item.previewUrl)}
                alt={item.asset?.filename ?? ""}
              />
            )}
            <div className={styles.lightboxFooter}>
              <Caption1 className={styles.muted}>{taken === undefined ? "" : formatWhen(taken)}</Caption1>
              <div className={styles.lightboxNav}>
                <Button icon={<ChevronLeftRegular />} disabledFocusable={onPrev === undefined} onClick={onPrev}>
                  Previous
                </Button>
                <Button
                  icon={<ChevronRightRegular />}
                  iconPosition="after"
                  disabledFocusable={onNext === undefined}
                  onClick={onNext}
                >
                  Next
                </Button>
              </div>
            </div>
          </DialogContent>
        </DialogBody>
      </DialogSurface>
    </Dialog>
  );
}

function Detail({
  item,
  picked,
  onOpenLightbox,
  onClose,
}: {
  item: LibraryItem;
  /** Whether the user picked the item, rather than it being the first one. */
  picked: boolean;
  onOpenLightbox: () => void;
  onClose: () => void;
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
    <Card className={mergeClasses(styles.detail, !picked && styles.detailUnpicked)} aria-label="Details">
      <div className={styles.detailHeader}>
        <div className={styles.detailTitle}>
          <Subtitle1 as="h2">{ref?.filename}</Subtitle1>
          <Caption1 block className={styles.muted}>
            {kindLabel[item.kind]}
            {taken !== undefined && ` · ${formatWhen(taken)}`}
          </Caption1>
        </div>
        <Button
          appearance="subtle"
          size="small"
          icon={<DismissRegular />}
          aria-label="Close details"
          onClick={onClose}
        />
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
