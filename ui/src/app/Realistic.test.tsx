import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { createRouterTransport } from "@connectrpc/connect";
import { render, screen } from "@testing-library/react";
import { StrictMode } from "react";
import { expect, it } from "vitest";

import {
  AgentService,
  AppleAccountState,
  AssetKind,
  AssetState,
  DownloadOutcome,
  LibraryItemSchema,
  ListRunsResponseSchema,
  OverviewSchema,
  Phase,
  RunResult,
  RunSchema,
  SettingsSchema,
} from "../gen/homecloud/agent/v3/agent_pb";
import { App } from "./App";
import type { Platform } from "./platformContext";
import { testPlatform } from "../test/platform";

const browser: Platform = testPlatform();

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
      listAssets: (req) => {
        onThisDay.push(req.onThisDay === undefined ? "" : `${req.onThisDay.month}-${req.onThisDay.day}`);
        return {
          items: [
            create(LibraryItemSchema, {
              asset: { library: "PrimarySync", assetId: "M1", filename: "IMG_0001.HEIC" },
              kind: AssetKind.PHOTO,
              createdAt: timestampFromDate(new Date(2023, 5, 20, 12)),
              state: AssetState.VERIFIED,
              thumbnailUrl: "/media/thumb/PrimarySync/M1/original?sig=x",
            }),
          ],
        };
      },
    });
  });
  const onThisDay: string[] = [];
  render(
    <StrictMode>
      <App transport={transport} platform={browser} />
    </StrictMode>,
  );
  expect(await screen.findByText("Your archive is complete and verified")).toBeInTheDocument();
  expect(await screen.findByText("Up to date")).toBeInTheDocument();
  const memory = await screen.findByRole("link", { name: "IMG_0001.HEIC, 2023" });
  expect(memory.getAttribute("href")).toBe("#/library?view=on-this-day");
  const now = new Date();
  expect(onThisDay[0]).toBe(`${now.getMonth() + 1}-${now.getDate()}`);
  expect(screen.queryByText(/Couldn.t load/)).not.toBeInTheDocument();
});
