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
  Spinner,
  Subtitle1,
  Text,
  ToggleButton,
  tokens,
} from "@fluentui/react-components";
import { FolderOpenRegular } from "@fluentui/react-icons";
import { useQueryClient } from "@tanstack/react-query";

import { LoadError } from "../components/LoadError";
import { useState } from "react";

import { usePlatform } from "../app/platformContext";
import { Page } from "../components/Page";
import { ReleaseDialog } from "../components/ReleaseDialog";
import { mediaLabel, tierLabel } from "../components/labels";
import { useNotify } from "../components/toast";
import { formatBytes, formatWhen, toDate } from "../format";
import { AgentService, type QuarantinedAsset, type Rendition } from "../gen/homecloud/agent/v2/agent_pb";

const useStyles = makeStyles({
  intro: { color: tokens.colorNeutralForeground3, maxWidth: "720px" },
  split: { display: "flex", flexWrap: "wrap", gap: tokens.spacingHorizontalL, alignItems: "flex-start" },
  list: { flex: "1 1 280px", padding: tokens.spacingHorizontalXS, gap: tokens.spacingVerticalXXS },
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
  mono: { fontFamily: tokens.fontFamilyMonospace, fontSize: tokens.fontSizeBase200 },
  head: { display: "flex", flexWrap: "wrap", alignItems: "center", gap: tokens.spacingHorizontalM },
  actions: { display: "flex", justifyContent: "flex-end" },
});

export function QuarantinePage() {
  const styles = useStyles();
  const list = useQuery(AgentService.method.listQuarantine, {});
  const [selected, setSelected] = useState<string | undefined>(undefined);
  const assets = list.data?.assets ?? [];
  const current = assets.find((a) => a.asset?.assetId === selected) ?? assets[0];

  return (
    <Page title="Quarantine">
      <Text className={styles.intro}>
        These files failed a check: when they were downloaded, they didn&apos;t match what iCloud says they should be,
        or a later re-check found them changed or missing on disk. They stay on disk untouched, and the matching photo
        or video is never deleted from iCloud while it&apos;s here.
      </Text>
      {list.isPending && <Spinner label="Loading…" />}
      {list.isError && <LoadError what="the quarantine" error={list.error} onRetry={() => void list.refetch()} />}
      {list.isSuccess && assets.length === 0 && (
        <MessageBar intent="success">
          <MessageBarBody>Nothing is in quarantine. Every archived file passed its checks.</MessageBarBody>
        </MessageBar>
      )}
      {current !== undefined && (
        <div className={styles.split}>
          <Card className={styles.list} role="list" aria-label="Quarantined items">
            {assets.map((a) => (
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
          </Card>
          <Detail item={current} />
        </div>
      )}
    </Page>
  );
}

function summary(a: QuarantinedAsset): string {
  const when = toDate(a.quarantinedAt);
  const tiers = [...new Set(a.failures.map((f) => tierLabel[f.tier]))].join(", ");
  return when === undefined ? tiers : `${tiers} · ${formatWhen(when)}`;
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
  const platform = usePlatform();
  const queryClient = useQueryClient();
  const ref = item.asset;
  const asset = useQuery(AgentService.method.getAsset, { library: ref?.library ?? "", assetId: ref?.assetId ?? "" });
  const release = useMutation(AgentService.method.releaseQuarantine, {
    onSuccess: async () => {
      notify("success", `Released ${ref?.filename ?? "item"}`, "All checks run again on the next sync.");
      await queryClient.invalidateQueries({
        queryKey: createConnectQueryKey({ schema: AgentService, cardinality: "finite" }),
      });
    },
    onError: (err) => {
      notify("error", "Couldn't release the item", err.message);
    },
  });

  return (
    <Card className={styles.detail}>
      <div className={styles.head}>
        <Subtitle1 as="h2">{ref?.filename}</Subtitle1>
        <Badge appearance="tint" color="danger">
          Quarantined
        </Badge>
      </div>
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
            {platform.kind === "desktop" && (
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
          </dd>
          <dt className={styles.dt}>Checks</dt>
          <dd className={styles.dd}>{checks(r)}</dd>
        </dl>
      ))}
      <div className={styles.actions}>
        <ReleaseDialog
          filename={ref?.filename ?? "this item"}
          pending={release.isPending}
          onRelease={(reason) =>
            release.mutateAsync({ library: ref?.library ?? "", assetId: ref?.assetId ?? "", reason })
          }
        />
      </div>
    </Card>
  );
}
