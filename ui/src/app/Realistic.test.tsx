import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { createRouterTransport } from "@connectrpc/connect";
import { render, screen } from "@testing-library/react";
import { StrictMode } from "react";
import { expect, it } from "vitest";

import {
  AgentService,
  AppleAccountState,
  DownloadOutcome,
  ListRunsResponseSchema,
  OverviewSchema,
  Phase,
  RunResult,
  RunSchema,
  SettingsSchema,
} from "../gen/homecloud/agent/v3/agent_pb";
import { App } from "./App";
import type { Platform } from "./platformContext";

const browser: Platform = { kind: "browser", mediaUrl: (path) => path, logError: (m) => expect.unreachable(m) };

const run = create(RunSchema, {
  id: 4n,
  kind: "cycle",
  startedAt: timestampFromDate(new Date(Date.now() - 60_000)),
  finishedAt: timestampFromDate(new Date(Date.now() - 55_000)),
  result: RunResult.OK,
  cycle: { download: DownloadOutcome.OK, assets: 3661n, renditions: 5687n },
});

it("renders a realistic overview in StrictMode without looping", async () => {
  const transport = createRouterTransport(({ service }) => {
    service(AgentService, {
      async *watchOverview() {
        yield {
          overview: create(OverviewSchema, {
            assets: { verified: 3661n },
            archive: { files: 5687n, bytes: 285_942_383_675n },
            storage: {
              root: "D:\\Archive",
              totalBytes: 999_286_689_792n,
              freeBytes: 518_929_182_720n,
              minFreePercent: 15,
            },
            appleAccount: { state: AppleAccountState.SIGNED_IN },
            activity: { phase: Phase.IDLE, nextCycleAt: timestampFromDate(new Date(Date.now() + 3_000_000)) },
            lastCycle: run,
          }),
        };
        await new Promise(() => undefined);
      },
      getSettings: () => ({ settings: create(SettingsSchema, { syncIntervalSeconds: 3600n }) }),
      listRuns: () => create(ListRunsResponseSchema, { runs: [run] }),
    });
  });
  render(
    <StrictMode>
      <App transport={transport} platform={browser} />
    </StrictMode>,
  );
  expect(await screen.findByText("Your archive is complete and verified")).toBeInTheDocument();
  expect(await screen.findByText("Up to date")).toBeInTheDocument();
});
