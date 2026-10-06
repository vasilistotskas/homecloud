import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { createRouterTransport } from "@connectrpc/connect";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import { App } from "../app/App";
import type { Platform } from "../app/platformContext";
import {
  AgentService,
  AssetKind,
  AssetState,
  GetAssetResponseSchema,
  LibraryItemSchema,
  OverviewSchema,
} from "../gen/homecloud/agent/v3/agent_pb";

const desktopLike: Platform = { kind: "browser", mediaUrl: (path) => "http://agent" + path };

function item(id: string, taken: Date, kind: AssetKind) {
  return create(LibraryItemSchema, {
    asset: { library: "PrimarySync", assetId: id, filename: `${id}.HEIC` },
    kind,
    createdAt: timestampFromDate(taken),
    state: AssetState.VERIFIED,
    thumbnailUrl: `/media/thumb/PrimarySync/${id}/original?sig=x`,
    previewUrl: `/media/preview/PrimarySync/${id}/original?sig=x`,
    displayVersion: "original",
  });
}

describe("Library", () => {
  it("groups thumbnails by month and lists hidden photos on request", async () => {
    const hiddenRequests: boolean[] = [];
    const transport = createRouterTransport(({ service }) => {
      service(AgentService, {
        async *watchOverview() {
          yield { overview: create(OverviewSchema, { archive: { files: 3n, bytes: 1024n } }) };
          await new Promise(() => undefined);
        },
        listAssets: (req) => {
          hiddenRequests.push(req.hidden);
          return req.hidden
            ? { items: [item("H1", new Date(2024, 0, 5), AssetKind.PHOTO)] }
            : {
                items: [
                  item("A1", new Date(2026, 9, 3), AssetKind.LIVE_PHOTO),
                  item("A2", new Date(2026, 9, 1), AssetKind.VIDEO),
                  item("A3", new Date(2026, 8, 20), AssetKind.PHOTO),
                ],
              };
        },
        getAsset: () => create(GetAssetResponseSchema),
      });
    });
    window.location.hash = "#/library";
    render(<App transport={transport} platform={desktopLike} />);

    expect(await screen.findByRole("region", { name: /October 2026/ })).toBeInTheDocument();
    expect(screen.getByRole("region", { name: /September 2026/ })).toBeInTheDocument();
    const tile = screen.getByRole("button", { name: /A2.HEIC/ });
    expect(tile.querySelector("img")?.getAttribute("src")).toBe(
      "http://agent/media/thumb/PrimarySync/A2/original?sig=x",
    );
    expect(screen.getByLabelText("Video")).toBeInTheDocument();

    await userEvent.setup().click(screen.getByRole("button", { name: "Hidden" }));
    expect(await screen.findByRole("button", { name: /H1.HEIC/ })).toBeInTheDocument();
    expect(hiddenRequests).toContain(true);
  });
});
