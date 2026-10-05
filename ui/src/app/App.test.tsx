import { create } from "@bufbuild/protobuf";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import {
  AgentService,
  AppleAccountState,
  DriveHealthState,
  ListRunsResponseSchema,
  OverviewSchema,
  Phase,
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

const settings = create(SettingsSchema, { syncIntervalSeconds: 3600n, storageRoot: "E:\\HomeCloud" });

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
