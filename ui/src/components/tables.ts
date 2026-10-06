import { makeStyles, tokens } from "@fluentui/react-components";

import { formatDuration, formatWhen, toDate } from "../format";
import type { Run } from "../gen/homecloud/agent/v1/agent_pb";

export const useTableStyles = makeStyles({
  tableBox: { overflowX: "auto" },
  num: { textAlign: "right", fontVariantNumeric: "tabular-nums" },
  // Header cells wrap their label in a flex container (the `button` slot); align it like the values.
  numHead: { justifyContent: "flex-end" },
  // Fluent tables default to equal fixed-width columns; size columns to their content instead.
  table: { tableLayout: "auto" },
  nowrap: { whiteSpace: "nowrap" },
  detail: { whiteSpace: "normal", minWidth: "260px" },
  mono: { fontFamily: tokens.fontFamilyMonospace, fontSize: tokens.fontSizeBase200 },
  more: { alignSelf: "flex-start" },
  muted: { color: tokens.colorNeutralForeground3 },
});

/** When a run started, for a table cell. */
export function started(run: Run): string {
  const at = toDate(run.startedAt);
  return at === undefined ? "—" : formatWhen(at);
}

/** How long a finished run took, for a table cell. */
export function took(run: Run): string {
  const from = toDate(run.startedAt);
  const to = toDate(run.finishedAt);
  return from !== undefined && to !== undefined ? formatDuration(to.getTime() - from.getTime()) : "—";
}

/** The page token of the next page of a paged list, if there is one. */
export function nextPage(last: { nextPageToken: string }): string | undefined {
  return last.nextPageToken === "" ? undefined : last.nextPageToken;
}
