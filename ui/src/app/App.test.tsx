import { create } from "@bufbuild/protobuf";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import {
  AgentService,
  AppleAccountState,
  DriveHealthState,
  DriveSyncOutcome,
  IPhoneBackupState,
  ListRunsResponseSchema,
  OverviewSchema,
  Phase,
  RunResult,
  SettingsSchema,
} from "../gen/homecloud/agent/v1/agent_pb";
import { App } from "./App";
import type { Platform } from "./platformContext";

const browser: Platform = { kind: "browser", mediaUrl: (path) => path };

function never(): Promise<never> {
  return new Promise<never>(() => undefined);
}

// failingStream is a server stream that fails before sending anything.
function failingStream<T>(err: Error): AsyncIterable<T> {
  return {
    [Symbol.asyncIterator]: () => ({ next: () => Promise.reject(err) }),
  };
}

const settings = create(SettingsSchema, {
  syncIntervalSeconds: 3600n,
  storageRoot: "E:\\HomeCloud",
  scrubIntervalSeconds: 604_800n,
  scrubFraction: 0.05,
});

describe("App", () => {
  it("shows the live overview", async () => {
    const transport = createRouterTransport(({ service }) => {
      service(AgentService, {
        async *watchOverview() {
          yield {
            overview: create(OverviewSchema, {
              assets: { verified: 3661n },
              archive: { files: 5687n, bytes: 285_942_383_675n },
              storage: {
                root: "E:\\HomeCloud",
                totalBytes: 999_286_689_792n,
                freeBytes: 518_929_182_720n,
                minFreePercent: 15,
              },
              appleAccount: { state: AppleAccountState.SIGNED_IN },
              activity: { phase: Phase.IDLE },
            }),
          };
          await never();
        },
        getSettings: () => ({ settings }),
        listRuns: () => create(ListRunsResponseSchema),
      });
    });
    render(<App transport={transport} platform={browser} />);
    expect(await screen.findByText("Your archive is complete and verified")).toBeInTheDocument();
    expect(screen.getByText("3,661")).toBeInTheDocument();
    expect(screen.getByText("266 GiB")).toBeInTheDocument();
    expect(screen.getByText("Agent connected")).toBeInTheDocument();
  });

  it("puts a failing archive drive first", async () => {
    const transport = createRouterTransport(({ service }) => {
      service(AgentService, {
        async *watchOverview() {
          yield {
            overview: create(OverviewSchema, {
              assets: { verified: 1n, quarantined: 1n },
              appleAccount: { state: AppleAccountState.SIGN_IN_REQUIRED },
              drive: { state: DriveHealthState.FAILING, reasons: ["drive reports: spare"], protocol: "NVMe" },
            }),
          };
          await never();
        },
        getSettings: () => ({ settings }),
        listRuns: () => create(ListRunsResponseSchema),
      });
    });
    render(<App transport={transport} platform={browser} />);
    expect(await screen.findByText("Your archive drive expects to fail")).toBeInTheDocument();
    expect(screen.getByText("Failing")).toBeInTheDocument();
  });

  it("reports archived files that changed on disk", async () => {
    const transport = createRouterTransport(({ service }) => {
      service(AgentService, {
        async *watchOverview() {
          yield {
            overview: create(OverviewSchema, {
              assets: { verified: 10n, quarantined: 2n },
              lastScrub: {
                result: RunResult.FAILED,
                finishedAt: { seconds: 1_790_000_000n },
                scrub: { eligible: 40n, checked: 2n, passed: 0n, mismatched: 2n },
              },
            }),
          };
          await never();
        },
        getSettings: () => ({ settings }),
        listRuns: () => create(ListRunsResponseSchema),
      });
    });
    render(<App transport={transport} platform={browser} />);
    expect(await screen.findByText("2 changed")).toBeInTheDocument();
    expect(await screen.findByText("re-read every 20 weeks")).toBeInTheDocument();
  });

  it("warns about an unencrypted iPhone backup", async () => {
    const transport = createRouterTransport(({ service }) => {
      service(AgentService, {
        async *watchOverview() {
          yield {
            overview: create(OverviewSchema, {
              assets: { verified: 10n },
              lastIphoneBackup: {
                result: RunResult.FAILED,
                finishedAt: { seconds: 1_790_000_000n },
                iphoneBackup: {
                  devices: [
                    {
                      udid: "U",
                      name: "Test iPhone",
                      productVersion: "26.1",
                      encrypted: false,
                      completedAt: { seconds: 1_789_990_000n },
                      files: 12n,
                      sizeBytes: 2048n,
                      state: IPhoneBackupState.UNENCRYPTED,
                      reasons: ["the backup is not encrypted"],
                    },
                  ],
                },
              },
            }),
          };
          await never();
        },
        getSettings: () => ({ settings }),
        listRuns: () => create(ListRunsResponseSchema),
      });
    });
    render(<App transport={transport} platform={browser} />);
    expect(await screen.findByText("An iPhone backup needs attention")).toBeInTheDocument();
    expect(screen.getAllByText("Not encrypted").length).toBeGreaterThan(0);
    expect(screen.getByText(/Test iPhone · iOS 26.1/)).toBeInTheDocument();
  });

  it("asks for a first iPhone backup when none exists", async () => {
    const transport = createRouterTransport(({ service }) => {
      service(AgentService, {
        async *watchOverview() {
          yield {
            overview: create(OverviewSchema, {
              assets: { verified: 10n },
              lastIphoneBackup: { result: RunResult.FAILED, finishedAt: { seconds: 1_790_000_000n }, iphoneBackup: {} },
            }),
          };
          await never();
        },
        getSettings: () => ({ settings }),
        listRuns: () => create(ListRunsResponseSchema),
      });
    });
    render(<App transport={transport} platform={browser} />);
    expect(await screen.findByText("No iPhone backup on this computer yet")).toBeInTheDocument();
    expect(screen.getByText("No backup yet")).toBeInTheDocument();
  });

  it("asks to sign homecloud in to iCloud Drive again with a command that runs", async () => {
    const transport = createRouterTransport(({ service }) => {
      service(AgentService, {
        async *watchOverview() {
          yield {
            overview: create(OverviewSchema, {
              assets: { verified: 10n },
              icloudDrive: {
                files: 23n,
                bytes: 46_068_308n,
                deletedInIcloud: 1n,
                signedInAt: { seconds: 1_790_000_000n },
              },
              lastDriveSync: {
                result: RunResult.FAILED,
                finishedAt: { seconds: 1_790_000_100n },
                driveSync: { outcome: DriveSyncOutcome.SIGN_IN_REQUIRED, error: "iCloud sign-in required" },
              },
            }),
          };
          await never();
        },
        getSettings: () => ({
          settings: create(SettingsSchema, {
            ...settings,
            cli: String.raw`C:\Program Files\homecloud\homecloud.exe`,
            configPath: String.raw`C:\cfg\config.toml`,
            driveSyncIntervalSeconds: 21_600n,
          }),
        }),
        getInfo: () => ({ agentVersion: "test", apiVersion: "v1", os: "windows" }),
        listRuns: () => create(ListRunsResponseSchema),
      });
    });
    render(<App transport={transport} platform={browser} />);
    expect(await screen.findByText("iCloud Drive sign-in needed")).toBeInTheDocument();
    expect(screen.getByText("23 files · 43.9 MiB")).toBeInTheDocument();
    expect(screen.getByText("1 (copies kept)")).toBeInTheDocument();
    // The banner and the iCloud Drive card both offer it.
    const [signIn] = screen.getAllByRole("button", { name: "Sign in to iCloud Drive" });
    if (signIn === undefined) {
      throw new Error("no sign-in button");
    }
    await userEvent.setup().click(signIn);
    const command = await screen.findByText(/'icloud' 'login' '--config'/);
    expect(command).toHaveTextContent(
      String.raw`& 'C:\Program Files\homecloud\homecloud.exe' 'icloud' 'login' '--config' 'C:\cfg\config.toml'`,
    );
  });

  it("shows the most severe problem when several are present", async () => {
    const transport = createRouterTransport(({ service }) => {
      service(AgentService, {
        async *watchOverview() {
          yield {
            overview: create(OverviewSchema, {
              assets: { verified: 10n, quarantined: 2n },
              appleAccount: { state: AppleAccountState.SIGN_IN_REQUIRED },
              lastDriveSync: {
                result: RunResult.FAILED,
                driveSync: { outcome: DriveSyncOutcome.SIGN_IN_REQUIRED },
              },
            }),
          };
          await never();
        },
        getSettings: () => ({ settings }),
        listRuns: () => create(ListRunsResponseSchema),
      });
    });
    render(<App transport={transport} platform={browser} />);
    expect(await screen.findByText("2 items failed verification")).toBeInTheDocument();
    expect(screen.queryByText("Apple sign-in needed")).not.toBeInTheDocument();
  });

  it("says the archive drive is missing when the agent reports it", async () => {
    const transport = createRouterTransport(({ service }) => {
      service(AgentService, {
        watchOverview: () =>
          failingStream(new ConnectError("archive drive not available: volume not mounted", Code.FailedPrecondition)),
        getSettings: () => ({ settings }),
      });
    });
    render(<App transport={transport} platform={browser} />);
    expect(await screen.findByText("The archive drive isn't available")).toBeInTheDocument();
    expect(screen.getByText(/volume not mounted/)).toBeInTheDocument();
    expect(screen.getByText("Archive drive unavailable")).toBeInTheDocument();
  });

  it("explains when the agent cannot be reached", async () => {
    const transport = createRouterTransport(({ service }) => {
      service(AgentService, {
        watchOverview: () => failingStream(new ConnectError("connection refused", Code.Unavailable)),
        getSettings: () => ({ settings }),
      });
    });
    render(<App transport={transport} platform={browser} />);
    expect(await screen.findByText("Can't reach the homecloud agent")).toBeInTheDocument();
    expect(screen.getByText("Agent not reachable")).toBeInTheDocument();
  });

  it("asks a browser without a session to connect", async () => {
    const transport = createRouterTransport(({ service }) => {
      service(AgentService, {
        watchOverview: () => failingStream(new ConnectError("missing or invalid credentials", Code.Unauthenticated)),
      });
    });
    render(<App transport={transport} platform={browser} />);
    expect(await screen.findByText("Connect this browser")).toBeInTheDocument();
  });

  it("signs in with the code from the link and shows the overview", async () => {
    let session = false;
    const exchanged: string[] = [];
    const transport = createRouterTransport(({ service }) => {
      service(AgentService, {
        watchOverview: () =>
          session
            ? (async function* () {
                yield { overview: create(OverviewSchema, { assets: { verified: 1n } }) };
                await never();
              })()
            : failingStream(new ConnectError("missing or invalid credentials", Code.Unauthenticated)),
        exchangeLoginCode: (req) => {
          exchanged.push(req.code);
          session = true;
          return {};
        },
        getSettings: () => ({ settings }),
        listRuns: () => create(ListRunsResponseSchema),
      });
    });
    window.location.hash = "#/connect?code=ABCD-EFGH";
    render(<App transport={transport} platform={browser} />);
    expect(await screen.findByText("Your archive is complete and verified")).toBeInTheDocument();
    expect(exchanged).toEqual(["ABCD-EFGH"]);
  });
});
