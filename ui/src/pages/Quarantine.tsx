import { createConnectQueryKey, useMutation, useQuery } from "@connectrpc/connect-query";
import {
  Badge,
  Body1,
  Button,
  Caption1,
  Card,
  makeStyles,
  MessageBar,
  MessageBarBody,
  SearchBox,
  Spinner,
  Subtitle1,
  Text,
  ToggleButton,
  tokens,
} from "@fluentui/react-components";
import { CopyRegular, FolderOpenRegular } from "@fluentui/react-icons";
import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { usePlatform } from "../app/platformContext";
import { LoadError } from "../components/LoadError";
import { Page } from "../components/Page";
import { ReleaseDialog } from "../components/ReleaseDialog";
import { RepairDialog } from "../components/RepairDialog";
import { mediaLabel, tierLabel } from "../components/labels";
import { useNotify } from "../components/toast";
import { useCopy } from "../components/useCopy";
import { formatBytes, formatWhen, toDate } from "../format";
import { AgentService, type Failure, type QuarantinedAsset, type Rendition } from "../gen/homecloud/agent/v3/agent_pb";

const useStyles = makeStyles({
  intro: { color: tokens.colorNeutralForeground3, maxWidth: "720px" },
  split: { display: "flex", flexWrap: "wrap", gap: tokens.spacingHorizontalL, alignItems: "flex-start" },
  list: { flex: "1 1 280px", padding: tokens.spacingHorizontalXS, gap: tokens.spacingVerticalXXS },
  items: { display: "flex", flexDirection: "column", gap: tokens.spacingVerticalXXS },
  item: {
    display: "flex",
    flexDirection: "column",
    alignItems: "flex-start",
    width: "100%",
    minHeight: "56px",
    textAlign: "left",
    border: "none",
  },
  detail: { flex: "999 1 480px", gap: tokens.spacingVerticalL },
  facts: {
    display: "grid",
    gridTemplateColumns: "auto 1fr",
    gap: `${tokens.spacingVerticalS} ${tokens.spacingHorizontalL}`,
    margin: 0,
  },
  dt: { color: tokens.colorNeutralForeground3 },
  dd: { margin: 0, minWidth: 0, overflowWrap: "anywhere" },
  pathActions: { display: "inline-flex", gap: tokens.spacingHorizontalXS, marginLeft: tokens.spacingHorizontalS },
  mono: { fontFamily: tokens.fontFamilyMonospace, fontSize: tokens.fontSizeBase200 },
  head: { display: "flex", flexWrap: "wrap", alignItems: "center", gap: tokens.spacingHorizontalM },
  actions: { display: "flex", justifyContent: "flex-end", gap: tokens.spacingHorizontalS },
});

export function QuarantinePage() {
  const styles = useStyles();
  const list = useQuery(AgentService.method.listQuarantine, {});
  const [selected, setSelected] = useState<string | undefined>(undefined);
  const [searchQuery, setSearchQuery] = useState("");
  const assets = list.data?.assets ?? [];

  const normalizedQuery = searchQuery.trim().toLowerCase();
  const filteredAssets =
    normalizedQuery === ""
      ? assets
      : assets.filter((a) => {
          const fn = a.asset?.filename ?? "";
          const s = summary(a);
          return fn.toLowerCase().includes(normalizedQuery) || s.toLowerCase().includes(normalizedQuery);
        });
  const current = filteredAssets.find((a) => a.asset?.assetId === selected) ?? filteredAssets[0];

  return (
    <Page title="Quarantine">
      <Text className={styles.intro}>
        These files failed a check: when they were downloaded, they didn&apos;t match what iCloud says they should be,
        or a later re-check found them changed or missing on disk. The matching photo or video is never deleted from
        iCloud while it&apos;s here. Release an item when the check was wrong, or repair it to download a damaged file
        again; the damaged file is kept in the archive&apos;s Damaged folder.
      </Text>
      {list.isPending && <Spinner label="Loading…" />}
      {list.isError && <LoadError what="the quarantine" error={list.error} onRetry={() => void list.refetch()} />}
      {list.isSuccess && assets.length === 0 && (
        <MessageBar intent="success">
          <MessageBarBody>Nothing is in quarantine. Every archived file passed its checks.</MessageBarBody>
        </MessageBar>
      )}
      {assets.length > 0 && (
        <div className={styles.split}>
          <Card className={styles.list}>
            <SearchBox
              aria-label="Filter quarantined items"
              placeholder="Filter by name or check…"
              value={searchQuery}
              onChange={(_, d) => {
                setSearchQuery(d.value);
              }}
            />
            {filteredAssets.length === 0 ? (
              <Text className={styles.dt}>No quarantined item matches.</Text>
            ) : (
              <div className={styles.items} role="list" aria-label="Quarantined items">
                {filteredAssets.map((a) => (
                  <div role="listitem" key={a.asset?.assetId}>
                    <ToggleButton
                      appearance="subtle"
                      className={styles.item}
                      checked={a === current}
                      onClick={() => {
                        setSelected(a.asset?.assetId);
                      }}
                    >
                      <Text weight="semibold">{a.asset?.filename}</Text>
                      <Caption1>{summary(a)}</Caption1>
                    </ToggleButton>
                  </div>
                ))}
              </div>
            )}
          </Card>
          {current !== undefined && <Detail item={current} />}
        </div>
      )}
    </Page>
  );
}

function summary(a: QuarantinedAsset): string {
  const when = toDate(a.quarantinedAt);
  const tiers = [...new Set(a.failures.map((f) => tierLabel[f.tier]))].join(", ");
  const text = when === undefined ? tiers : `${tiers} · ${formatWhen(when)}`;
  return a.repairRequestedAt === undefined ? text : `${text} · repairing`;
}

/** Where the repair of f stands. */
function repairProgress(f: Failure, root: string | undefined): string {
  if (f.setAsideAt === undefined) {
    return `${f.version}: moved aside at the next sync, then downloaded again.`;
  }
  if (f.keptAs === "") {
    return `${f.version}: the file was missing; waiting for kei to download it again.`;
  }
  return `${f.version}: moved to ${root === undefined ? f.keptAs : `${root}/${f.keptAs}`}; waiting for kei to download it again.`;
}

function checks(r: Rendition): string {
  const passed = new Set(r.passes.map((p) => p.tier));
  return r.requiredTiers
    .map((t) => `${tierLabel[t]} ${passed.has(t) ? "✓" : r.failure?.tier === t ? "✗" : "…"}`)
    .join(" · ");
}

function Detail({ item }: { item: QuarantinedAsset }) {
  const styles = useStyles();
  const notify = useNotify();
  const copy = useCopy();
  const platform = usePlatform();
  const queryClient = useQueryClient();
  const ref = item.asset;
  const asset = useQuery(AgentService.method.getAsset, { library: ref?.library ?? "", assetId: ref?.assetId ?? "" });
  const settings = useQuery(AgentService.method.getSettings, {});
  const invalidate = () =>
    queryClient.invalidateQueries({
      queryKey: createConnectQueryKey({ schema: AgentService, cardinality: "finite" }),
    });
  const release = useMutation(AgentService.method.releaseQuarantine, {
    onSuccess: async () => {
      notify("success", `Released ${ref?.filename ?? "item"}`, "All checks run again on the next sync.");
      await invalidate();
    },
    onError: (err) => {
      notify("error", "Couldn't release the item", err.message);
    },
  });
  const repair = useMutation(AgentService.method.repairQuarantine, {
    onSuccess: async () => {
      notify("success", `Repairing ${ref?.filename ?? "item"}`, "The next sync downloads it again.");
      await invalidate();
    },
    onError: (err) => {
      notify("error", "Couldn't start the repair", err.message);
    },
  });
  const repairing = item.repairRequestedAt !== undefined;
  const requested = toDate(item.repairRequestedAt);

  return (
    <Card className={styles.detail}>
      <div className={styles.head}>
        <Subtitle1 as="h2">{ref?.filename}</Subtitle1>
        <Badge appearance="tint" color="danger">
          Quarantined
        </Badge>
        {repairing && (
          <Badge appearance="tint" color="informative">
            Repairing
          </Badge>
        )}
      </div>
      {repairing && (
        <MessageBar intent="info">
          <MessageBarBody>
            Repair requested{requested === undefined ? "" : ` ${formatWhen(requested)}`}.{" "}
            {item.failures.map((f) => repairProgress(f, settings.data?.settings?.storageRoot)).join(" ")}
          </MessageBarBody>
        </MessageBar>
      )}
      {item.failures.map((f) => (
        <Body1 key={f.version}>
          <strong>
            {tierLabel[f.tier]} check failed ({f.version}):
          </strong>{" "}
          {f.reason}
        </Body1>
      ))}
      {asset.isPending && <Spinner size="tiny" />}
      {asset.isError && <LoadError what="this item's files" error={asset.error} onRetry={() => void asset.refetch()} />}
      {asset.data?.asset?.renditions.map((r) => (
        <dl className={styles.facts} key={r.version}>
          <dt className={styles.dt}>Version</dt>
          <dd className={styles.dd}>
            {r.version} · {mediaLabel[r.mediaType]}
          </dd>
          <dt className={styles.dt}>Size</dt>
          <dd className={styles.dd}>{formatBytes(r.sizeBytes)}</dd>
          <dt className={styles.dt}>On disk</dt>
          <dd className={`${styles.dd} ${styles.mono}`}>
            {r.localPath}
            <span className={styles.pathActions}>
              {platform.kind === "desktop" && platform.connection.kind === "this_pc" && (
                <Button
                  size="small"
                  appearance="subtle"
                  icon={<FolderOpenRegular />}
                  onClick={() => {
                    platform.revealInFolder(r.localPath).catch((err: unknown) => {
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
                  copy(r.localPath, "Path");
                }}
              >
                Copy path
              </Button>
            </span>
          </dd>
          <dt className={styles.dt}>Checks</dt>
          <dd className={styles.dd}>{checks(r)}</dd>
        </dl>
      ))}
      {!repairing && (
        <div className={styles.actions}>
          <RepairDialog
            filename={ref?.filename ?? "this item"}
            pending={repair.isPending}
            onRepair={() => repair.mutateAsync({ library: ref?.library ?? "", assetId: ref?.assetId ?? "" })}
          />
          <ReleaseDialog
            filename={ref?.filename ?? "this item"}
            pending={release.isPending}
            onRelease={(reason) =>
              release.mutateAsync({ library: ref?.library ?? "", assetId: ref?.assetId ?? "", reason })
            }
          />
        </div>
      )}
    </Card>
  );
}
