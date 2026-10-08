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

const desktopLike: Platform = {
  kind: "browser",
  mediaUrl: (path) => "http://agent" + path,
  logError: (m) => expect.unreachable(m),
};

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

  it("navigates photos with arrow keys and opens lightbox preview", async () => {
    const transport = createRouterTransport(({ service }) => {
      service(AgentService, {
        async *watchOverview() {
          yield { overview: create(OverviewSchema, { archive: { files: 3n, bytes: 1024n } }) };
          await new Promise(() => undefined);
        },
        listAssets: () => ({
          items: [
            item("A1", new Date(2026, 9, 3), AssetKind.PHOTO),
            item("A2", new Date(2026, 9, 2), AssetKind.PHOTO),
            item("A3", new Date(2026, 9, 1), AssetKind.PHOTO),
          ],
        }),
        getAsset: () => create(GetAssetResponseSchema),
      });
    });
    window.location.hash = "#/library";
    render(<App transport={transport} platform={desktopLike} />);

    // Initially A1 is selected
    expect(await screen.findByRole("heading", { level: 2, name: "A1.HEIC" })).toBeInTheDocument();

    // ArrowRight navigates to next item (A2)
    await userEvent.keyboard("{ArrowRight}");
    expect(await screen.findByRole("heading", { level: 2, name: "A2.HEIC" })).toBeInTheDocument();

    // ArrowLeft navigates back to A1
    await userEvent.keyboard("{ArrowLeft}");
    expect(await screen.findByRole("heading", { level: 2, name: "A1.HEIC" })).toBeInTheDocument();

    // Open lightbox
    const enlargeBtn = screen.getByRole("button", { name: "Enlarge photo" });
    await userEvent.setup().click(enlargeBtn);

    // Lightbox dialog should open with title, image, and navigation buttons
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Next photo" })).toBeInTheDocument();
    expect(screen.getByText(/1 of 3/)).toBeInTheDocument();

    // Click next photo in lightbox
    await userEvent.setup().click(screen.getByRole("button", { name: "Next photo" }));
    expect(screen.getByText(/2 of 3/)).toBeInTheDocument();
  });

  it("toggles grid density and saves to localStorage", async () => {
    localStorage.clear();
    const transport = createRouterTransport(({ service }) => {
      service(AgentService, {
        async *watchOverview() {
          yield { overview: create(OverviewSchema, { archive: { files: 1n, bytes: 1024n } }) };
          await new Promise(() => undefined);
        },
        listAssets: () => ({
          items: [item("A1", new Date(2026, 9, 3), AssetKind.PHOTO)],
        }),
        getAsset: () => create(GetAssetResponseSchema),
      });
    });
    window.location.hash = "#/library";
    render(<App transport={transport} platform={desktopLike} />);

    expect(await screen.findByRole("button", { name: "Compact" })).toBeInTheDocument();
    const compactBtn = screen.getByRole("button", { name: "Compact" });
    await userEvent.setup().click(compactBtn);
    expect(localStorage.getItem("homecloud.gridDensity")).toBe("compact");

    const spaciousBtn = screen.getByRole("button", { name: "Spacious" });
    await userEvent.setup().click(spaciousBtn);
    expect(localStorage.getItem("homecloud.gridDensity")).toBe("spacious");
  });
});
