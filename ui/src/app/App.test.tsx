import { create } from "@bufbuild/protobuf";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import {
  AgentService,
  AppleAccountState,
  HealthState,
  DriveSyncOutcome,
  IPhoneBackupState,
  ListRunsResponseSchema,
  OverviewSchema,
  Phase,
  RunResult,
  SettingsSchema,
} from "../gen/homecloud/agent/v3/agent_pb";
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

  it("shows the overview for an address that names no page, without a history entry", async () => {
    const transport = createRouterTransport(({ service }) => {
      service(AgentService, {
        async *watchOverview() {
          yield { overview: create(OverviewSchema, { assets: { verified: 1n } }) };
          await never();
        },
        getSettings: () => ({ settings }),
        listRuns: () => create(ListRunsResponseSchema),
      });
    });
    window.location.hash = "#/no-such-page";
    const before = window.history.length;
    render(<App transport={transport} platform={browser} />);
    expect(await screen.findByText("Your archive is complete and verified")).toBeInTheDocument();
    expect(window.location.hash).toBe("#/");
    expect(window.history.length).toBe(before);
    // A real link: keyboard, middle-click and "open in new tab" work.
    expect(screen.getByRole("link", { name: "View all activity" })).toHaveAttribute("href", "#/activity");
  });

  it("starts every page at its top", async () => {
    const transport = createRouterTransport(({ service }) => {
      service(AgentService, {
        async *watchOverview() {
          yield { overview: create(OverviewSchema, { assets: { verified: 1n } }) };
          await never();
        },
        getSettings: () => ({ settings }),
        listRuns: () => create(ListRunsResponseSchema),
        listDevices: () => ({}),
      });
    });
    const scrollTo = vi.spyOn(window, "scrollTo");
    render(<App transport={transport} platform={browser} />);
    await screen.findByText("Your archive is complete and verified");
    scrollTo.mockClear();
    await userEvent.setup().click(screen.getByRole("button", { name: "Settings" }));
    await expect.poll(() => window.location.hash).toBe("#/settings");
    await expect.poll(() => scrollTo.mock.calls).toContainEqual([0, 0]);
    scrollTo.mockRestore();
  });

  it("puts failing archive drives first", async () => {
    const transport = createRouterTransport(({ service }) => {
      service(AgentService, {
        async *watchOverview() {
          yield {
            overview: create(OverviewSchema, {
              assets: { verified: 1n, quarantined: 1n },
              appleAccount: { state: AppleAccountState.SIGN_IN_REQUIRED },
              storageHealth: {
                state: HealthState.FAILING,
                reasons: ["nvme0n1: drive reports: spare"],
                drives: [
                  { name: "nvme0n1", state: HealthState.FAILING, reasons: ["drive reports: spare"], protocol: "NVMe" },
                ],
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
    expect(await screen.findByText("Your archive's drives need attention now")).toBeInTheDocument();
    expect(screen.getByText("Failing")).toBeInTheDocument();
  });

  it("shows every drive and RAID array under the archive", async () => {
    const transport = createRouterTransport(({ service }) => {
      service(AgentService, {
        async *watchOverview() {
          yield {
            overview: create(OverviewSchema, {
              assets: { verified: 1n },
              storageHealth: {
                state: HealthState.FAILING,
                reasons: ["sdb: no device node here", "md1: lost redundancy: 1 of 2 drives missing or failed"],
                drives: [
                  {
                    name: "sda",
                    state: HealthState.OK,
                    model: "ST4000VN006-3CW104",
                    serial: "ZW6123AB",
                    protocol: "ATA",
                    temperatureCelsius: 34,
                    mediaErrors: 0n,
                  },
                  { name: "sdb", state: HealthState.UNAVAILABLE, reasons: ["no device node here"] },
                ],
                arrays: [
                  {
                    name: "md1",
                    state: HealthState.FAILING,
                    reasons: ["lost redundancy: 1 of 2 drives missing or failed"],
                    level: "raid1",
                    arrayState: "active",
                    raidDisks: 2,
                    degraded: 1,
                    syncAction: "recover",
                    syncDone: 1024n,
                    syncTotal: 4096n,
                    members: [
                      { name: "sda3", drive: "sda", states: ["in_sync"] },
                      { name: "sdb3", drive: "sdb", states: ["spare"] },
                    ],
                  },
                ],
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
    expect(await screen.findByText("ST4000VN006-3CW104 · ATA")).toBeInTheDocument();
    expect(screen.getByText("ZW6123AB")).toBeInTheDocument();
    expect(screen.getByText("Health can't be read for this drive: no device node here")).toBeInTheDocument();
    expect(screen.getByText("RAID 1 · active")).toBeInTheDocument();
    expect(screen.getByText("1 of 2 in sync (sda, sdb)")).toBeInTheDocument();
    expect(screen.getByText("Rebuilding · 25% done")).toBeInTheDocument();
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

  it("shows the archive's copies of a pushed iPhone backup", async () => {
    const transport = createRouterTransport(({ service }) => {
      service(AgentService, {
        async *watchOverview() {
          yield {
            overview: create(OverviewSchema, {
              assets: { verified: 10n },
              lastIphoneBackup: {
                result: RunResult.OK,
                finishedAt: { seconds: 1_790_000_000n },
                iphoneBackup: {
                  devices: [
                    {
                      udid: "U",
                      name: "Test iPhone",
                      productVersion: "26.1",
                      encrypted: true,
                      completedAt: { seconds: 1_789_990_000n },
                      files: 12n,
                      sizeBytes: 2048n,
                      state: IPhoneBackupState.OK,
                      fromCopy: true,
                      copies: [
                        { completedAt: { seconds: 1_789_990_000n }, files: 12n, sizeBytes: 2048n, copiedBytes: 512n },
                        { completedAt: { seconds: 1_789_900_000n }, files: 12n, sizeBytes: 2048n, copiedBytes: 2048n },
                      ],
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
    expect(await screen.findByText("Pushed from another computer")).toBeInTheDocument();
    expect(screen.getByText(/^2 in the archive · newest .* · 2.5 KiB on disk$/)).toBeInTheDocument();
    expect(screen.getByText("Every file checked")).toBeInTheDocument();
  });

  it("warns about a damaged iPhone backup copy", async () => {
    const transport = createRouterTransport(({ service }) => {
      service(AgentService, {
        async *watchOverview() {
          yield {
            overview: create(OverviewSchema, {
              assets: { verified: 10n },
              lastIphoneBackup: {
                result: RunResult.OK,
                finishedAt: { seconds: 1_790_000_000n },
                iphoneBackup: {
                  devices: [
                    {
                      udid: "U",
                      name: "Test iPhone",
                      productVersion: "26.1",
                      encrypted: true,
                      completedAt: { seconds: 1_789_990_000n },
                      files: 12n,
                      sizeBytes: 2048n,
                      state: IPhoneBackupState.OK,
                      fromCopy: true,
                      copies: [
                        {
                          completedAt: { seconds: 1_789_990_000n },
                          files: 12n,
                          sizeBytes: 2048n,
                          copiedBytes: 2048n,
                          damagedFiles: 2n,
                        },
                      ],
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
    expect(await screen.findByText("An iPhone backup copy in the archive is damaged")).toBeInTheDocument();
    expect(screen.getByText("Damaged copies")).toBeInTheDocument();
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

  it("shows a toast without covering the app", async () => {
    const transport = createRouterTransport(({ service }) => {
      service(AgentService, {
        async *watchOverview() {
          yield { overview: create(OverviewSchema, { assets: { verified: 1n } }) };
          await never();
        },
        getSettings: () => ({
          settings: create(SettingsSchema, { ...settings, notifyTopic: "homecloud_topic_for_tests_123" }),
        }),
        getInfo: () => ({ agentVersion: "test", apiVersion: "v1", os: "windows" }),
      });
    });
    const user = userEvent.setup();
    window.location.hash = "#/settings";
    const { container } = render(<App transport={transport} platform={browser} />);
    await user.click(await screen.findByRole("button", { name: "Copy" }));
    const toast = await screen.findByText("Topic copied");

    // The toast's portal carries the theme but not the page's own styles
    // (full height, page background), which would hide the whole app.
    const page = container.querySelector(".fui-FluentProvider");
    const portal = [...document.body.children].find((e) => e.contains(toast));
    if (page === null || portal === undefined) {
      throw new Error("no page or portal element");
    }
    const pageOnly = [...page.classList].filter((c) => !c.startsWith("fui-FluentProvider"));
    expect(pageOnly.length).toBeGreaterThan(0);
    expect([...portal.classList].filter((c) => pageOnly.includes(c))).toEqual([]);
    window.location.hash = "";
  });

  it("lists paired devices and revokes one", async () => {
    const revoked: string[] = [];
    const transport = createRouterTransport(({ service }) => {
      service(AgentService, {
        async *watchOverview() {
          yield { overview: create(OverviewSchema, { assets: { verified: 1n } }) };
          await never();
        },
        getSettings: () => ({ settings }),
        getInfo: () => ({ agentVersion: "test", apiVersion: "v1", os: "linux" }),
        listDevices: () => ({
          devices: revoked.includes("d1")
            ? []
            : [
                {
                  id: "d1",
                  name: "Laptop",
                  pairedAt: { seconds: 1_790_000_000n },
                  lastSeenAt: { seconds: 1_790_000_600n },
                },
              ],
        }),
        revokeDevice: (req) => {
          revoked.push(req.deviceId);
          return {};
        },
      });
    });
    const user = userEvent.setup();
    window.location.hash = "#/settings";
    render(<App transport={transport} platform={browser} />);
    await user.click(await screen.findByRole("button", { name: "Revoke Laptop" }));
    await user.click(await screen.findByRole("button", { name: "Revoke" }));
    expect(await screen.findByText("None")).toBeInTheDocument();
    expect(revoked).toEqual(["d1"]);
    window.location.hash = "";
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
