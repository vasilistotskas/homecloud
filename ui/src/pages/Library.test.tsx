import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { createRouterTransport } from "@connectrpc/connect";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
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

  it("selects the focused tile, in a grid the arrow keys move through", async () => {
    window.location.hash = "#/library";
    render(<App transport={library([["A1", "A2", "A3"]])} platform={desktopLike} />);
    expect(await screen.findByRole("heading", { level: 2, name: "A1.HEIC" })).toBeInTheDocument();

    // Tabster moves the focus between tiles; jsdom has no layout for it to
    // move through, so the test checks the grid is a mover and focuses a tile.
    const grid = screen.getByRole("region", { name: /October 2026/ }).parentElement;
    expect(grid?.getAttribute("data-tabster")).toContain("mover");
    fireEvent.focus(screen.getByRole("button", { name: /A2.HEIC/ }));
    expect(await screen.findByRole("heading", { level: 2, name: "A2.HEIC" })).toBeInTheDocument();
  });

  it("steps through the library in the lightbox, loading the next page at the end of the loaded one", async () => {
    window.location.hash = "#/library";
    render(<App transport={library([["A1", "A2"], ["B1"]])} platform={desktopLike} />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Enlarge photo" }));

    const dialog = await screen.findByRole("dialog");
    const title = () => within(dialog).getByRole("heading", { level: 2 });
    expect(title()).toHaveTextContent("A1.HEIC");
    expect(within(dialog).getByRole("img")).toHaveAttribute(
      "src",
      "http://agent/media/preview/PrimarySync/A1/original?sig=x",
    );
    expect(within(dialog).getByRole("button", { name: "Previous" })).toHaveAttribute("aria-disabled", "true");

    await user.keyboard("{ArrowRight}");
    expect(title()).toHaveTextContent("A2.HEIC");
    await user.click(within(dialog).getByRole("button", { name: "Next" }));
    await waitFor(() => {
      expect(title()).toHaveTextContent("B1.HEIC");
    });
    expect(within(dialog).getByRole("button", { name: "Next" })).toHaveAttribute("aria-disabled", "true");
    await user.keyboard("{ArrowLeft}");
    expect(title()).toHaveTextContent("A2.HEIC");
  });

  it("keeps the grid density chosen in the view options", async () => {
    localStorage.clear();
    window.location.hash = "#/library";
    const { unmount } = render(<App transport={library([["A1"]])} platform={desktopLike} />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Library view options" }));
    await user.click(await screen.findByRole("menuitemradio", { name: "Compact" }));
    unmount();

    render(<App transport={library([["A1"]])} platform={desktopLike} />);
    await user.click(await screen.findByRole("button", { name: "Library view options" }));
    expect(await screen.findByRole("menuitemradio", { name: "Compact" })).toHaveAttribute("aria-checked", "true");
  });

  it("can close and reopen the details panel", async () => {
    window.location.hash = "#/library";
    render(<App transport={library([["A1"]])} platform={desktopLike} />);
    const user = userEvent.setup();
    expect(await screen.findByRole("heading", { level: 2, name: "A1.HEIC" })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Close details" }));
    expect(screen.queryByRole("heading", { level: 2, name: "A1.HEIC" })).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Toggle details panel" }));
    expect(await screen.findByRole("heading", { level: 2, name: "A1.HEIC" })).toBeInTheDocument();
  });
});

/** An agent whose library has the given pages of items, newest first. */
function library(pages: string[][]) {
  return createRouterTransport(({ service }) => {
    service(AgentService, {
      async *watchOverview() {
        yield { overview: create(OverviewSchema, { archive: { files: 3n, bytes: 1024n } }) };
        await new Promise(() => undefined);
      },
      listAssets: (req) => {
        const page = req.pageToken === "" ? 0 : Number(req.pageToken);
        return {
          items: (pages[page] ?? []).map((id, i) => item(id, new Date(2026, 9, 28 - page * 10 - i), AssetKind.PHOTO)),
          nextPageToken: page + 1 < pages.length ? String(page + 1) : "",
        };
      },
      getAsset: () => create(GetAssetResponseSchema),
    });
  });
}
