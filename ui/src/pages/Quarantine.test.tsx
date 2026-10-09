import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { createRouterTransport } from "@connectrpc/connect";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import { App } from "../app/App";
import type { Platform } from "../app/platformContext";
import { navigate } from "../app/router";
import {
  AgentService,
  AppleAccountState,
  GetAssetResponseSchema,
  GetSettingsResponseSchema,
  ListQuarantineResponseSchema,
  MediaType,
  OverviewSchema,
  Phase,
  QuarantinedAssetSchema,
  SettingsSchema,
  Tier,
} from "../gen/homecloud/agent/v3/agent_pb";

const desktopLike: Platform = {
  kind: "browser",
  mediaUrl: (path) => "http://agent" + path,
  logError: (m) => expect.unreachable(m),
};

function quarantinedItem(id: string, filename: string) {
  return create(QuarantinedAssetSchema, {
    asset: {
      library: "PrimarySync",
      assetId: id,
      filename,
    },
    quarantinedAt: timestampFromDate(new Date(2026, 9, 1)),
    failures: [{ tier: Tier.CONTENT, version: "original", reason: "checksum mismatch" }],
  });
}

describe("Quarantine", () => {
  it("filters quarantined items with search box and displays details", async () => {
    const transport = createRouterTransport(({ service }) => {
      service(AgentService, {
        async *watchOverview() {
          yield {
            overview: create(OverviewSchema, {
              appleAccount: { state: AppleAccountState.SIGNED_IN },
              activity: { phase: Phase.IDLE },
              assets: { quarantined: 2n },
            }),
          };
          await new Promise(() => undefined);
        },
        getSettings: () => create(GetSettingsResponseSchema, { settings: create(SettingsSchema) }),
        listQuarantine: () =>
          create(ListQuarantineResponseSchema, {
            assets: [quarantinedItem("Q1", "IMG_0001.HEIC"), quarantinedItem("Q2", "corrupt_video.MOV")],
          }),
        getAsset: (req) =>
          create(GetAssetResponseSchema, {
            asset: {
              ref: { library: req.library, assetId: req.assetId, filename: "corrupt_video.MOV" },
              renditions: [
                {
                  version: "original",
                  localPath: "D:\\Archive\\Photos\\corrupt_video.MOV",
                  mediaType: MediaType.VIDEO,
                  sizeBytes: 2048n,
                  sha256: "abc123hash",
                  requiredTiers: [Tier.CONTENT],
                  passes: [],
                },
              ],
            },
          }),
      });
    });

    navigate("/quarantine");
    render(<App transport={transport} platform={desktopLike} />);

    // Both items visible initially in the list
    expect(await screen.findByRole("button", { name: /IMG_0001.HEIC/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /corrupt_video.MOV/ })).toBeInTheDocument();

    // Filter by "corrupt"
    const searchBox = screen.getByRole("searchbox", { name: "Filter quarantined items" });
    const user = userEvent.setup();
    await user.type(searchBox, "corrupt");

    // "IMG_0001.HEIC" should now be filtered out, "corrupt_video.MOV" should remain
    expect(screen.queryByRole("button", { name: /IMG_0001.HEIC/ })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /corrupt_video.MOV/ })).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 2, name: "corrupt_video.MOV" })).toBeInTheDocument();

    // Verify Copy path button is present in Detail
    expect(screen.getByRole("button", { name: "Copy path" })).toBeInTheDocument();

    // Filter with no matches
    await user.clear(searchBox);
    await user.type(searchBox, "nonexistent");
    expect(screen.getByText("No quarantined item matches.")).toBeInTheDocument();
  });

  it("repairs an item and shows the repair's progress", async () => {
    const repaired: string[] = [];
    let item = quarantinedItem("Q1", "IMG_0001.HEIC");
    const transport = createRouterTransport(({ service }) => {
      service(AgentService, {
        async *watchOverview() {
          yield { overview: create(OverviewSchema, { activity: { phase: Phase.IDLE }, assets: { quarantined: 1n } }) };
          await new Promise(() => undefined);
        },
        getSettings: () =>
          create(GetSettingsResponseSchema, { settings: create(SettingsSchema, { storageRoot: "E:/HomeCloud" }) }),
        listQuarantine: () => create(ListQuarantineResponseSchema, { assets: [item] }),
        getAsset: (req) =>
          create(GetAssetResponseSchema, { asset: { ref: { library: req.library, assetId: req.assetId } } }),
        repairQuarantine: (req) => {
          repaired.push(req.assetId);
          item = create(QuarantinedAssetSchema, {
            ...item,
            repairRequestedAt: timestampFromDate(new Date(2026, 9, 9, 10)),
            failures: item.failures.map((f) => ({
              ...f,
              setAsideAt: timestampFromDate(new Date(2026, 9, 9, 10, 1)),
              keptAs: "Damaged/20261009T070000Z/2022/06/IMG_0001.HEIC",
            })),
          });
          return {};
        },
      });
    });

    navigate("/quarantine");
    render(<App transport={transport} platform={desktopLike} />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Repair…" }));
    await user.click(await screen.findByRole("button", { name: "Download again" }));

    expect(
      await screen.findByText(
        /original: moved to E:\/HomeCloud\/Damaged\/20261009T070000Z\/2022\/06\/IMG_0001\.HEIC; waiting/,
      ),
    ).toBeInTheDocument();
    expect(repaired).toEqual(["Q1"]);
    expect(screen.getAllByText("Repairing").length).toBeGreaterThan(0);
    // An item under repair is neither released nor repaired again.
    expect(screen.queryByRole("button", { name: "Repair…" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Release…" })).not.toBeInTheDocument();
  });
});
