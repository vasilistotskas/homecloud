import { useInfiniteQuery } from "@connectrpc/connect-query";
import {
  Badge,
  Button,
  Card,
  Spinner,
  Table,
  TableBody,
  TableCell,
  TableHeader,
  TableHeaderCell,
  TableRow,
  Text,
} from "@fluentui/react-components";
import type { ReactNode } from "react";

import { AgentService, RunResult, type RunKind, type Run } from "../gen/homecloud/agent/v1/agent_pb";
import { LoadError } from "./LoadError";
import { nextPage, started, took, useTableStyles } from "./tables";

export function MoreButton({
  q,
}: {
  q: { hasNextPage: boolean; isFetchingNextPage: boolean; fetchNextPage: () => unknown };
}) {
  const styles = useTableStyles();
  if (!q.hasNextPage) {
    return null;
  }
  return (
    <Button
      className={styles.more}
      disabled={q.isFetchingNextPage}
      onClick={() => {
        void q.fetchNextPage();
      }}
    >
      {q.isFetchingNextPage ? "Loading…" : "Show older"}
    </Button>
  );
}

/** The result badge of a run: running, ok or failed, with the given labels. */
export function RunBadge({ run, ok, failed }: { run: Run; ok: string; failed: string }) {
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
          {ok}
        </Badge>
      );
    case RunResult.FAILED:
      return (
        <Badge appearance="tint" color="danger">
          {failed}
        </Badge>
      );
    case RunResult.UNSPECIFIED:
      return null;
  }
}

/** One column of a run history: its header and its cell for a run. */
export interface RunColumn {
  header: string;
  numeric?: boolean;
  cell: (run: Run) => ReactNode;
}

/**
 * The history of one kind of run: when each started, the given columns, how
 * long it took and its result, newest first, loaded a page at a time.
 */
export function RunTable({
  kind,
  noun,
  empty,
  columns,
  result,
}: {
  kind: RunKind;
  /** What the runs are, for loading and error messages ("integrity checks"). */
  noun: string;
  empty: string;
  columns: RunColumn[];
  result: (run: Run) => ReactNode;
}) {
  const styles = useTableStyles();
  const q = useInfiniteQuery(
    AgentService.method.listRuns,
    { kind, pageToken: "" },
    { pageParamKey: "pageToken", getNextPageParam: nextPage },
  );
  if (q.isPending) {
    return <Spinner label={`Loading ${noun}…`} />;
  }
  if (q.isError) {
    return <LoadError what={noun} error={q.error} onRetry={() => void q.refetch()} />;
  }
  const runs = q.data.pages.flatMap((p) => p.runs);
  return (
    <>
      <Card className={styles.tableBox}>
        <Table size="small" aria-label={noun.charAt(0).toUpperCase() + noun.slice(1)} className={styles.table}>
          <TableHeader>
            <TableRow>
              <TableHeaderCell>Started</TableHeaderCell>
              {columns.map((c) => (
                <TableHeaderCell key={c.header} button={c.numeric === true ? { className: styles.numHead } : undefined}>
                  {c.header}
                </TableHeaderCell>
              ))}
              <TableHeaderCell button={{ className: styles.numHead }}>Took</TableHeaderCell>
              <TableHeaderCell>Result</TableHeaderCell>
            </TableRow>
          </TableHeader>
          <TableBody>
            {runs.map((run) => (
              <TableRow key={run.id.toString()}>
                <TableCell className={styles.nowrap}>{started(run)}</TableCell>
                {columns.map((c) => (
                  <TableCell key={c.header} className={c.numeric === true ? styles.num : styles.detail}>
                    {c.cell(run)}
                  </TableCell>
                ))}
                <TableCell className={styles.num}>{took(run)}</TableCell>
                <TableCell className={styles.detail}>{result(run)}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
        {runs.length === 0 && <Text className={styles.muted}>{empty}</Text>}
      </Card>
      <MoreButton q={q} />
    </>
  );
}
