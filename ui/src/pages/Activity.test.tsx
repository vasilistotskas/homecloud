import { create } from "@bufbuild/protobuf";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";

import { App } from "../app/App";
import type { Platform } from "../app/platformContext";
import {
  AgentService,
  DriveFileChange,
  DriveSyncOutcome,
  EventKind,
  ListEventsResponseSchema,
  IPhoneBackupState,
  ListRunsResponseSchema,
  OverviewSchema,
  RunKind,
  RunResult,
  SettingsSchema,
  Tier,
} from "../gen/homecloud/agent/v3/agent_pb";

const browser: Platform = { kind: "browser", mediaUrl: (path) => path, logError: (m) => expect.unreachable(m) };

function never(): Promise<never> {
  return new Promise<never>(() => undefined);
}

afterEach(() => {
  window.location.hash = "";
});

describe("Activity", () => {
  it("shows why syncs could not be listed instead of claiming there are none", async () => {
    const transport = createRouterTransport(({ service }) => {
      service(AgentService, {
        async *watchOverview() {
          yield { overview: create(OverviewSchema, {}) };
          await never();
        },
        getSettings: () => ({ settings: create(SettingsSchema, {}) }),
        listRuns: () => {
          throw new ConnectError("unknown run kind RUN_KIND_UNSPECIFIED", Code.InvalidArgument);
        },
      });
    });
    window.location.hash = "#/activity";
    render(<App transport={transport} platform={browser} />);
    expect(await screen.findByText("Couldn't load syncs")).toBeInTheDocument();
    expect(screen.getByText(/unknown run kind/)).toBeInTheDocument();
    expect(screen.queryByText("No syncs yet.")).not.toBeInTheDocument();
  });

  it("lists integrity checks", async () => {
    const transport = createRouterTransport(({ service }) => {
      service(AgentService, {
        async *watchOverview() {
          yield { overview: create(OverviewSchema, {}) };
          await never();
        },
        getSettings: () => ({ settings: create(SettingsSchema, {}) }),
        listRuns: (req) =>
          create(ListRunsResponseSchema, {
            runs:
              req.kind === RunKind.SCRUB
                ? [
                    {
                      id: 7n,
                      kind: "scrub",
                      result: RunResult.FAILED,
                      startedAt: { seconds: 1_790_000_000n },
                      finishedAt: { seconds: 1_790_000_060n },
                      scrub: { eligible: 6040n, checked: 302n, passed: 301n, mismatched: 1n },
                    },
                  ]
                : [],
          }),
      });
    });
    window.location.hash = "#/activity";
    render(<App transport={transport} platform={browser} />);
    await userEvent.setup().click(await screen.findByRole("tab", { name: "Integrity checks" }));
    expect(await screen.findByText("302 of 6,040")).toBeInTheDocument();
    expect(screen.getByText("1 quarantined")).toBeInTheDocument();
  });

  it("lists iPhone backup checks", async () => {
    const transport = createRouterTransport(({ service }) => {
      service(AgentService, {
        async *watchOverview() {
          yield { overview: create(OverviewSchema, {}) };
          await never();
        },
        getSettings: () => ({ settings: create(SettingsSchema, {}) }),
        listRuns: (req) =>
          create(ListRunsResponseSchema, {
            runs:
              req.kind === RunKind.IPHONE_BACKUP
                ? [
                    {
                      id: 9n,
                      kind: "iphone_backup",
                      result: RunResult.FAILED,
                      startedAt: { seconds: 1_790_000_000n },
                      finishedAt: { seconds: 1_790_000_030n },
                      iphoneBackup: {
                        devices: [
                          {
                            udid: "U",
                            name: "Test iPhone",
                            state: IPhoneBackupState.STALE,
                            reasons: ["the last backup finished 10 days ago, more than 7 days"],
                          },
                        ],
                      },
                    },
                  ]
                : [],
          }),
      });
    });
    window.location.hash = "#/activity";
    render(<App transport={transport} platform={browser} />);
    await userEvent.setup().click(await screen.findByRole("tab", { name: "iPhone backups" }));
    expect(await screen.findByText("Out of date")).toBeInTheDocument();
    expect(screen.getByText("Needs attention")).toBeInTheDocument();
  });

  it("lists iCloud Drive syncs and their failures", async () => {
    const transport = createRouterTransport(({ service }) => {
      service(AgentService, {
        async *watchOverview() {
          yield { overview: create(OverviewSchema, {}) };
          await never();
        },
        getSettings: () => ({ settings: create(SettingsSchema, {}) }),
        listRuns: (req) =>
          create(ListRunsResponseSchema, {
            runs:
              req.kind === RunKind.DRIVE_SYNC
                ? [
                    {
                      id: 11n,
                      kind: "drive_sync",
                      result: RunResult.FAILED,
                      startedAt: { seconds: 1_790_000_000n },
                      finishedAt: { seconds: 1_790_000_040n },
                      driveSync: {
                        outcome: DriveSyncOutcome.PARTIAL,
                        listed: 23n,
                        archived: 22n,
                        failed: 1n,
                        failures: ["iCloud Drive/x.pdf: downloaded bytes do not match the signature"],
                      },
                    },
                  ]
                : [],
          }),
      });
    });
    window.location.hash = "#/activity";
    render(<App transport={transport} platform={browser} />);
    await userEvent.setup().click(await screen.findByRole("tab", { name: "iCloud Drive" }));
    expect(await screen.findByText(/x.pdf: downloaded bytes do not match/)).toBeInTheDocument();
    expect(screen.getByText("Some items failed")).toBeInTheDocument();
  });

  it("describes iCloud Drive events", async () => {
    const transport = createRouterTransport(({ service }) => {
      service(AgentService, {
        async *watchOverview() {
          yield { overview: create(OverviewSchema, {}) };
          await never();
        },
        getSettings: () => ({ settings: create(SettingsSchema, { driveDir: "D:/Archive/Drive" }) }),
        listRuns: () => create(ListRunsResponseSchema),
        listEvents: (req) =>
          create(ListEventsResponseSchema, {
            events: req.kinds.includes(EventKind.DRIVE_FILE_MOVED)
              ? [
                  {
                    id: 5n,
                    at: { seconds: 1_790_000_000n },
                    asset: { library: "icloud_drive", assetId: "FILE::z::A", filename: "b.pdf" },
                    detail: {
                      case: "driveFile",
                      value: {
                        change: DriveFileChange.MOVED,
                        path: "iCloud Drive/Docs/b.pdf",
                        fromPath: "iCloud Drive/a.pdf",
                      },
                    },
                  },
                  {
                    id: 6n,
                    at: { seconds: 1_790_000_100n },
                    asset: { library: "icloud_drive", assetId: "FILE::z::C", filename: "c.pdf" },
                    detail: {
                      case: "driveFile",
                      value: {
                        change: DriveFileChange.REPAIRED,
                        path: "iCloud Drive/c.pdf",
                        keptAs: ".versions/20261006T120000Z/iCloud Drive/c.pdf",
                      },
                    },
                  },
                ]
              : [],
          }),
      });
    });
    window.location.hash = "#/activity";
    render(<App transport={transport} platform={browser} />);
    await userEvent.setup().click(await screen.findByRole("tab", { name: "Events" }));
    expect(await screen.findByText("iCloud Drive/a.pdf → iCloud Drive/Docs/b.pdf")).toBeInTheDocument();
    expect(screen.getByText("Repaired")).toBeInTheDocument();
    expect(
      screen.getByText(
        "iCloud Drive/c.pdf; the previous copy is kept at D:/Archive/Drive/.versions/20261006T120000Z/iCloud Drive/c.pdf",
      ),
    ).toBeInTheDocument();
  });

  it("deep-links directly to integrity tab from hash query param", async () => {
    const transport = createRouterTransport(({ service }) => {
      service(AgentService, {
        async *watchOverview() {
          yield { overview: create(OverviewSchema, {}) };
          await never();
        },
        getSettings: () => ({ settings: create(SettingsSchema, {}) }),
        listRuns: (req) =>
          create(ListRunsResponseSchema, {
            runs:
              req.kind === RunKind.SCRUB
                ? [
                    {
                      id: 7n,
                      kind: "scrub",
                      result: RunResult.FAILED,
                      startedAt: { seconds: 1_790_000_000n },
                      finishedAt: { seconds: 1_790_000_060n },
                      scrub: { eligible: 6040n, checked: 302n, passed: 301n, mismatched: 1n },
                    },
                  ]
                : [],
          }),
      });
    });
    window.location.hash = "#/activity?tab=integrity";
    render(<App transport={transport} platform={browser} />);
    expect(await screen.findByText("302 of 6,040")).toBeInTheDocument();
  });

  it("filters events by search query", async () => {
    const transport = createRouterTransport(({ service }) => {
      service(AgentService, {
        async *watchOverview() {
          yield { overview: create(OverviewSchema, {}) };
          await never();
        },
        getSettings: () => ({ settings: create(SettingsSchema, {}) }),
        listEvents: () =>
          create(ListEventsResponseSchema, {
            events: [
              {
                id: 10n,
                at: { seconds: 1_790_000_000n },
                asset: { library: "icloud_drive", assetId: "FILE::z::X", filename: "invoice.pdf" },
                detail: {
                  case: "driveFile",
                  value: {
                    change: DriveFileChange.ARCHIVED,
                    path: "iCloud Drive/invoice.pdf",
                    sizeBytes: 2048n,
                  },
                },
              },
              {
                id: 11n,
                at: { seconds: 1_790_000_100n },
                asset: { library: "photos", assetId: "PHOTO::z::Y", filename: "vacation.heic" },
                detail: {
                  case: "quarantined",
                  value: { tier: Tier.CONTENT, reason: "corrupted stream" },
                },
              },
            ],
          }),
      });
    });
    window.location.hash = "#/activity?tab=events";
    render(<App transport={transport} platform={browser} />);
    expect(await screen.findByText("invoice.pdf")).toBeInTheDocument();
    expect(screen.getByText("vacation.heic")).toBeInTheDocument();

    const searchInput = screen.getByPlaceholderText("Search events…");
    await userEvent.setup().type(searchInput, "invoice");

    expect(screen.getByText("invoice.pdf")).toBeInTheDocument();
    expect(screen.queryByText("vacation.heic")).not.toBeInTheDocument();
  });
});
