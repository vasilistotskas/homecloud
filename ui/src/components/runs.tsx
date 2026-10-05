import { Badge } from "@fluentui/react-components";

import { DownloadOutcome, RunResult, type Run } from "../gen/homecloud/agent/v1/agent_pb";
import { formatCount, formatDuration, formatWhen, toDate } from "../format";

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

function downloadLabel(run: Run): string {
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
      const mismatched = run.cycle?.mismatched ?? 0n;
      return (
        <Badge appearance="tint" color="danger">
          {mismatched > 0n ? `${formatCount(mismatched)} quarantined` : "Failed"}
        </Badge>
      );
    }
    case RunResult.UNSPECIFIED:
      return "—";
  }
}

export function runRow(run: Run): RunRow {
  const started = toDate(run.startedAt);
  const finished = toDate(run.finishedAt);
  const c = run.cycle;
  const n = (v: bigint | undefined) => (v === undefined ? "—" : formatCount(v));
  return {
    id: run.id.toString(),
    started: started === undefined ? "—" : formatWhen(started),
    download: downloadLabel(run),
    inICloud: n(c?.assets),
    added: n(c?.newRenditions),
    changed: n(c?.changedRenditions),
    checked: n(c?.checked),
    failed: n(c === undefined ? undefined : c.mismatched + c.errored),
    took:
      started !== undefined && finished !== undefined ? formatDuration(finished.getTime() - started.getTime()) : "—",
    result: resultBadge(run),
  };
}
