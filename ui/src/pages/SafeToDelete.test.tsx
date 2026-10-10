import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { createRouterTransport } from "@connectrpc/connect";
import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { App } from "../app/App";
import type { Platform } from "../app/platformContext";
import { navigate } from "../app/router";
import {
  AgentService,
  AssetKind,
  GetSafeToDeleteResponseSchema,
  ListSafeToDeleteResponseSchema,
  OverviewSchema,
  Phase,
  SafeToDeleteItemSchema,
  SafeToDeleteSchema,
} from "../gen/homecloud/agent/v3/agent_pb";

const browser: Platform = {
  kind: "browser",
  mediaUrl: (path) => "http://agent" + path,
  logError: (m) => expect.unreachable(m),
};

const year = 365n * 24n * 3600n;

function item(id: string, filename: string, taken: Date, sizeBytes: bigint) {
  return create(SafeToDeleteItemSchema, {
    item: {
      asset: { library: "PrimarySync", assetId: id, filename },
      kind: AssetKind.PHOTO,
      createdAt: timestampFromDate(taken),
      thumbnailUrl: `/media/thumb/PrimarySync/${id}/original?sig=x`,
    },
    sizeBytes,
    path: `E:\\HomeCloud\\Photos\\${filename}`,
  });
}

function transportWith(report: ReturnType<typeof create<typeof SafeToDeleteSchema>>, zones: string[]) {
  return createRouterTransport(({ service }) => {
    service(AgentService, {
      async *watchOverview() {
        yield { overview: create(OverviewSchema, { activity: { phase: Phase.IDLE } }) };
        await new Promise(() => undefined);
      },
      getSafeToDelete: (req) => {
        zones.push(req.timeZone);
        return create(GetSafeToDeleteResponseSchema, { report });
      },
      listSafeToDelete: () => {
        expect(report.withheld).toBe("");
        return create(ListSafeToDeleteResponseSchema, {
          items: [
            item("A", "IMG_0002.HEIC", new Date(2024, 4, 15, 12), 3_000_000n),
            item("B", "IMG_0001.HEIC", new Date(2024, 2, 10, 12), 1_000_000n),
          ],
        });
      },
    });
  });
}

describe("Safe to delete", () => {
  it("lists the items by month with what each month frees, and what is not listed yet", async () => {
    const zones: string[] = [];
    const report = create(SafeToDeleteSchema, {
      assets: 2n,
      bytes: 4_000_000n,
      months: [
        { year: 2024, month: 5, assets: 1n, bytes: 3_000_000n },
        { year: 2024, month: 3, assets: 1n, bytes: 1_000_000n },
      ],
      awaitingScrub: 1204n,
      tooRecent: 12n,
      notVerified: 0n,
      minAgeSeconds: year,
      exportPath: "E:\\HomeCloud\\safe-to-delete.csv",
    });
    navigate("/safe-to-delete");
    render(<App transport={transportWith(report, zones)} platform={browser} />);

    const may = await screen.findByRole("region", { name: "May 2024" });
    expect(within(may).getByText("IMG_0002.HEIC")).toBeInTheDocument();
    expect(within(may).getByText(/^1 item · /)).toBeInTheDocument();
    const march = screen.getByRole("region", { name: "March 2024" });
    expect(within(march).getByText("IMG_0001.HEIC")).toBeInTheDocument();
    expect(screen.getByText(/^2 items · /)).toBeInTheDocument();

    expect(screen.getByText(/more than 365 days ago/)).toBeInTheDocument();
    expect(screen.getByText(/^1,204 not checked again on disk yet/)).toBeInTheDocument();
    expect(screen.getByText(/^12 added to iCloud less than 365 days ago/)).toBeInTheDocument();
    expect(screen.queryByText(/not verified yet/)).not.toBeInTheDocument();
    expect(screen.getByText("E:\\HomeCloud\\safe-to-delete.csv")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Copy path" })).toBeInTheDocument();
    // The months are counted in the viewer's own time zone.
    expect(zones[0]).toBe(Intl.DateTimeFormat().resolvedOptions().timeZone);
  });

  it("lists nothing and says why while the archive drive is not healthy", async () => {
    const report = create(SafeToDeleteSchema, {
      withheld: "the archive drive's health is warning: nvme0: 2 media errors",
      awaitingScrub: 3n,
      minAgeSeconds: year,
      exportPath: "E:\\HomeCloud\\safe-to-delete.csv",
    });
    navigate("/safe-to-delete");
    render(<App transport={transportWith(report, [])} platform={browser} />);

    expect(
      await screen.findByText(/Nothing is listed: the archive drive's health is warning: nvme0: 2 media errors/),
    ).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: /2024/ })).not.toBeInTheDocument();
    expect(screen.queryByText(/ items · /)).not.toBeInTheDocument();
  });
});
