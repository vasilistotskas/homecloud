import { create } from "@bufbuild/protobuf";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";

import { App } from "../app/App";
import type { Platform } from "../app/platformContext";
import {
  AgentService,
  IPhoneBackupState,
  ListRunsResponseSchema,
  OverviewSchema,
  RunKind,
  RunResult,
  SettingsSchema,
} from "../gen/homecloud/agent/v1/agent_pb";

const browser: Platform = { kind: "browser", mediaUrl: (path) => path };

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
});
