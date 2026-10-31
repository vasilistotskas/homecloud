import {
  Button,
  Caption1,
  Card,
  makeStyles,
  mergeClasses,
  ProgressBar,
  Text,
  tokens,
} from "@fluentui/react-components";
import { DismissRegular, PauseRegular, PlayRegular } from "@fluentui/react-icons";
import { useEffect } from "react";

import {
  cancelUpload,
  clearFinishedUploads,
  pauseUpload,
  resumeUpload,
  uploadsPending,
  useUploads,
  type UploadItem,
} from "../api/uploads";
import { formatBytes } from "../format";

const useStyles = makeStyles({
  card: { gap: tokens.spacingVerticalS },
  head: { display: "flex", alignItems: "center", gap: tokens.spacingHorizontalS },
  title: { marginRight: "auto" },
  row: {
    display: "grid",
    gridTemplateColumns: "1fr auto",
    alignItems: "center",
    gap: `${tokens.spacingVerticalXXS} ${tokens.spacingHorizontalS}`,
  },
  name: { overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", minWidth: 0 },
  status: { gridColumn: "1 / -1", color: tokens.colorNeutralForeground3 },
  failed: { color: tokens.colorPaletteRedForeground1 },
  bar: { gridColumn: "1 / -1" },
  actions: { display: "flex" },
});

function status(u: UploadItem): string {
  const where = u.dir === "" ? u.location : `${u.location}/${u.dir}`;
  switch (u.state) {
    case "queued":
      return `Waiting · ${formatBytes(u.size)} → ${where}`;
    case "uploading":
      return `${formatBytes(u.sent)} of ${formatBytes(u.size)} → ${where}`;
    case "paused":
      return `Paused at ${formatBytes(u.sent)} of ${formatBytes(u.size)}`;
    case "done":
      return u.savedAs === undefined ? `Saved in ${where}` : `Saved as ${u.location}/${u.savedAs}`;
    case "failed":
      return u.error ?? "Failed";
  }
}

/** The uploads of this session, with their progress and controls. */
export function UploadList() {
  const styles = useStyles();
  const uploads = useUploads();

  // Leaving or reloading the page stops the uploads (they resume when the
  // same files are chosen again), so the browser asks first.
  useEffect(() => {
    const warn = (e: BeforeUnloadEvent) => {
      if (uploadsPending()) {
        e.preventDefault();
      }
    };
    window.addEventListener("beforeunload", warn);
    return () => {
      window.removeEventListener("beforeunload", warn);
    };
  }, []);

  if (uploads.length === 0) {
    return null;
  }
  const finished = uploads.some((u) => u.state === "done");
  return (
    <Card className={styles.card} aria-label="Uploads">
      <div className={styles.head}>
        <Text weight="semibold" className={styles.title}>
          Uploads
        </Text>
        {finished && (
          <Button size="small" appearance="subtle" onClick={clearFinishedUploads}>
            Clear finished
          </Button>
        )}
      </div>
      {uploads.map((u) => (
        <div key={u.id} className={styles.row}>
          <Text className={styles.name} title={u.name}>
            {u.name}
          </Text>
          <div className={styles.actions}>
            {u.state === "uploading" && (
              <Button
                size="small"
                appearance="subtle"
                icon={<PauseRegular />}
                aria-label={`Pause ${u.name}`}
                onClick={() => {
                  pauseUpload(u.id);
                }}
              />
            )}
            {(u.state === "paused" || u.state === "failed") && (
              <Button
                size="small"
                appearance="subtle"
                icon={<PlayRegular />}
                aria-label={`Resume ${u.name}`}
                onClick={() => {
                  resumeUpload(u.id);
                }}
              />
            )}
            {u.state !== "done" && (
              <Button
                size="small"
                appearance="subtle"
                icon={<DismissRegular />}
                aria-label={`Cancel ${u.name}`}
                onClick={() => {
                  cancelUpload(u.id);
                }}
              />
            )}
          </div>
          {u.state !== "done" && u.state !== "failed" && (
            <ProgressBar className={styles.bar} value={u.size === 0 ? 0 : u.sent / u.size} />
          )}
          <Caption1 className={mergeClasses(styles.status, u.state === "failed" && styles.failed)}>
            {status(u)}
          </Caption1>
        </div>
      ))}
    </Card>
  );
}
