import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import { App } from "../app/App";
import { navigate } from "../app/router";
import { formatBytes } from "../format";
import {
  AgentService,
  FileEntrySchema,
  ListFileLocationsResponseSchema,
  ListFilesResponseSchema,
  ListTrashResponseSchema,
  OverviewSchema,
  Phase,
} from "../gen/homecloud/agent/v3/agent_pb";
import { testPlatform } from "../test/platform";

const month = 30n * 24n * 3600n;

function entry(name: string, folder: boolean, sizeBytes = 0n) {
  return create(FileEntrySchema, {
    name,
    folder,
    sizeBytes,
    modifiedAt: timestampFromDate(new Date(2026, 9, 30, 12)),
  });
}

interface Calls {
  listed: string[];
  created: { path: string; name: string }[];
  trashed: string[][];
  links: { path: string; inline: boolean }[];
}

function agent(
  calls: Calls,
  opts: { locations?: { name: string; available: boolean }[]; createError?: ConnectError } = {},
) {
  const locations = opts.locations ?? [
    { name: "Uploads", available: true },
    { name: "Videos", available: true },
  ];
  return createRouterTransport(({ service }) => {
    service(AgentService, {
      async *watchOverview() {
        yield { overview: create(OverviewSchema, { activity: { phase: Phase.IDLE } }) };
        await new Promise(() => undefined);
      },
      listFileLocations: () =>
        create(ListFileLocationsResponseSchema, {
          locations: locations.map((l) => ({
            ...l,
            freeBytes: 500_000_000_000n,
            uploadPath: `/files/uploads/${l.name}/`,
          })),
          trashRetentionSeconds: month,
        }),
      listFiles: (req) => {
        calls.listed.push(`${req.location}:${req.path}`);
        if (req.path === "") {
          return create(ListFilesResponseSchema, {
            entries: [entry("Phone", true), entry("report.pdf", false, 2_000_000n)],
          });
        }
        return create(ListFilesResponseSchema, { entries: [entry("IMG_0001.HEIC", false, 3_000_000n)] });
      },
      createFolder: (req) => {
        if (opts.createError !== undefined) {
          throw opts.createError;
        }
        calls.created.push({ path: req.path, name: req.name });
        return {};
      },
      trashFiles: (req) => {
        calls.trashed.push(req.paths);
        return { trashed: req.paths.length };
      },
      listTrash: () =>
        create(ListTrashResponseSchema, {
          items: [{ id: "abc", path: "Phone/old.txt", sizeBytes: 5n, deletedAt: timestampFromDate(new Date()) }],
        }),
      getFileLink: (req) => {
        calls.links.push({ path: req.path, inline: req.inline });
        return { url: `/files/download/Uploads/${req.path}?sig=x${req.inline ? "&inline=1" : ""}` };
      },
    });
  });
}

function newCalls(): Calls {
  return { listed: [], created: [], trashed: [], links: [] };
}

describe("Files", () => {
  it("lists a location's top, folders first, and opens a folder", async () => {
    const calls = newCalls();
    navigate("/files");
    render(<App transport={agent(calls)} platform={testPlatform()} />);
    const list = await screen.findByRole("list", { name: "Files and folders" });
    const rows = await within(list).findAllByRole("listitem");
    expect(rows.map((r) => r.textContent)).toEqual([
      expect.stringContaining("Phone"),
      expect.stringContaining("report.pdf"),
    ]);
    expect(screen.getByText(`${formatBytes(500_000_000_000n)} free`)).toBeInTheDocument();

    await userEvent.click(within(list).getByRole("button", { name: /^Phone/ }));
    expect(await screen.findByText("IMG_0001.HEIC")).toBeInTheDocument();
    expect(calls.listed).toContain("Uploads:Phone");
    expect(window.location.hash).toBe("#/files?loc=Uploads&path=Phone");
  });

  it("creates a folder where it is, and shows the agent's reason when it refuses", async () => {
    const calls = newCalls();
    navigate("/files", { search: { loc: "Uploads", path: "Phone" } });
    render(
      <App
        transport={agent(calls, { createError: new ConnectError('already exists: "Phone/Trip"', Code.AlreadyExists) })}
        platform={testPlatform()}
      />,
    );
    await userEvent.click(await screen.findByRole("button", { name: "New folder" }));
    await userEvent.type(screen.getByRole("textbox", { name: /Name/ }), "Trip");
    await userEvent.click(screen.getByRole("button", { name: "Create" }));
    expect(await screen.findByText('already exists: "Phone/Trip"')).toBeInTheDocument();
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });

  it("deletes the selected items to the trash after confirming", async () => {
    const calls = newCalls();
    navigate("/files", { search: { loc: "Uploads" } });
    render(<App transport={agent(calls)} platform={testPlatform()} />);
    await userEvent.click(await screen.findByRole("checkbox", { name: "Select report.pdf" }));
    await userEvent.click(screen.getByRole("button", { name: "Delete" }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("restore it for 30 days");
    await userEvent.click(within(dialog).getByRole("button", { name: "Delete" }));
    await waitFor(() => {
      expect(calls.trashed).toEqual([["report.pdf"]]);
    });
  });

  it("offers a file's signed download and open links from its menu", async () => {
    const calls = newCalls();
    navigate("/files", { search: { loc: "Uploads" } });
    render(<App transport={agent(calls)} platform={testPlatform("http://agent")} />);
    await userEvent.click(await screen.findByRole("button", { name: "Actions for report.pdf" }));
    const download = await screen.findByRole("menuitem", { name: "Download" });
    await waitFor(() => {
      expect(download).toHaveAttribute("href", "http://agent/files/download/Uploads/report.pdf?sig=x");
    });
    expect(screen.getByRole("menuitem", { name: "Open" })).toHaveAttribute(
      "href",
      "http://agent/files/download/Uploads/report.pdf?sig=x&inline=1",
    );
  });

  it("shows the trash with how long items stay", async () => {
    navigate("/files", { search: { loc: "Uploads", view: "trash" } });
    render(<App transport={agent(newCalls())} platform={testPlatform()} />);
    expect(await screen.findByText("Phone/old.txt")).toBeInTheDocument();
    expect(screen.getByText(/stay here for 30 days/)).toBeInTheDocument();
  });

  it("says how to add a location when none is named", async () => {
    navigate("/files");
    render(<App transport={agent(newCalls(), { locations: [] })} platform={testPlatform()} />);
    expect(await screen.findByText("No folders are shared here yet")).toBeInTheDocument();
  });

  it("says when a location's folder cannot be opened", async () => {
    navigate("/files");
    render(
      <App
        transport={agent(newCalls(), { locations: [{ name: "NAS", available: false }] })}
        platform={testPlatform()}
      />,
    );
    expect(await screen.findByText("NAS is not available")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Upload" })).not.toBeInTheDocument();
  });
});
