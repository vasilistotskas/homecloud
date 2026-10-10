import { Badge } from "@fluentui/react-components";

import { DownloadOutcome, RunResult, type Run } from "../gen/homecloud/agent/v3/agent_pb";
import { formatCount } from "../format";
import { started, took } from "./tables";

/** One sync cycle, formatted for tables. */
export interface RunRow {
  id: string;
  started: string;
  download: string;
  inICloud: string;
  added: string;
  changed: string;
  checked: string;
  failed: string;
  took: string;
  result: React.ReactNode;
}

/** What the download did, and whether the downloader kept its place in iCloud. */
function downloadLabel(run: Run): string {
  const outcome = downloadOutcomeLabel(run);
  return run.cycle?.checkpoint?.blocked === true ? `${outcome} · place in iCloud kept` : outcome;
}

function downloadOutcomeLabel(run: Run): string {
  const c = run.cycle;
  if (c === undefined) {
    return run.result === RunResult.RUNNING ? "In progress" : "—";
  }
  switch (c.download) {
    case DownloadOutcome.OK:
      return c.newRenditions === 0n ? "Up to date" : `${formatCount(c.newRenditions)} new`;
    case DownloadOutcome.PARTIAL:
      return "Partly failed, retrying";
    case DownloadOutcome.SIGN_IN_REQUIRED:
      return "Sign-in needed";
    case DownloadOutcome.NEEDS_ATTENTION:
      return "Account needs attention";
    case DownloadOutcome.ERROR:
      return "Couldn't run";
    case DownloadOutcome.SKIPPED_LOW_SPACE:
      return "Skipped: drive almost full";
    case DownloadOutcome.ARCHIVE_UNAVAILABLE:
      return "Archive drive unavailable";
    case DownloadOutcome.INTERRUPTED:
      return "Interrupted";
    case DownloadOutcome.UNSPECIFIED:
      return "—";
  }
}

function resultBadge(run: Run): React.ReactNode {
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
          OK
        </Badge>
      );
    case RunResult.FAILED: {
      const c = run.cycle;
      const label =
        c === undefined
          ? "Failed"
          : c.mismatched > 0n
            ? `${formatCount(c.mismatched)} quarantined`
            : c.errored > 0n
              ? `${formatCount(c.errored)} not checked`
              : c.download === DownloadOutcome.INTERRUPTED
                ? "Interrupted"
                : "Failed";
      return (
        <Badge appearance="tint" color="danger">
          {label}
        </Badge>
      );
    }
    case RunResult.UNSPECIFIED:
      return "—";
  }
}

export function runRow(run: Run): RunRow {
  // A cycle that was interrupted, or stopped because the archive drive was
  // missing, recorded no counts: they are unknown, not zero.
  const c =
    run.cycle?.download === DownloadOutcome.INTERRUPTED || run.cycle?.download === DownloadOutcome.ARCHIVE_UNAVAILABLE
      ? undefined
      : run.cycle;
  const n = (v: bigint | undefined) => (v === undefined ? "—" : formatCount(v));
  return {
    id: run.id.toString(),
    started: started(run),
    download: downloadLabel(run),
    inICloud: n(c?.assets),
    added: n(c?.newRenditions),
    changed: n(c?.changedRenditions),
    checked: n(c?.checked),
    failed: n(c === undefined ? undefined : c.mismatched + c.errored),
    took: took(run),
    result: resultBadge(run),
  };
}
