import { create } from "@bufbuild/protobuf";
import { createRouterTransport } from "@connectrpc/connect";
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import { App } from "../app/App";
import type { Platform } from "../app/platformContext";
import {
  AgentService,
  AppleAccountState,
  GetSettingsResponseSchema,
  ListRunsResponseSchema,
  OverviewSchema,
  Phase,
  SettingsSchema,
} from "../gen/homecloud/agent/v3/agent_pb";

const desktopLike: Platform = {
  kind: "browser",
  mediaUrl: (path) => "http://agent" + path,
  logError: (m) => expect.unreachable(m),
};

describe("CommandPalette", () => {
  it("opens with keyboard shortcut and navigates to Activity integrity tab", async () => {
    const transport = createRouterTransport(({ service }) => {
      service(AgentService, {
        async *watchOverview() {
          yield {
            overview: create(OverviewSchema, {
              appleAccount: { state: AppleAccountState.SIGNED_IN },
              activity: { phase: Phase.IDLE },
            }),
          };
          await new Promise(() => undefined);
        },
        getSettings: () => create(GetSettingsResponseSchema, { settings: create(SettingsSchema) }),
        listRuns: () => create(ListRunsResponseSchema),
      });
    });

    window.location.hash = "#/";
    render(<App transport={transport} platform={desktopLike} />);

    // Press Ctrl+K
    fireEvent.keyDown(window, { key: "k", ctrlKey: true });

    const searchBox = await screen.findByPlaceholderText("Type a page, command or action…");
    expect(searchBox).toBeInTheDocument();

    // Type "integrity"
    const user = userEvent.setup();
    await user.type(searchBox, "integrity");

    const option = await screen.findByRole("option", { name: /Activity: Integrity checks/ });
    expect(option).toBeInTheDocument();

    await user.click(option);

    // Command palette closes and hash updates
    expect(window.location.hash).toBe("#/activity?tab=integrity");
  });

  it("triggers sync action from command palette", async () => {
    let syncStarted = false;
    const transport = createRouterTransport(({ service }) => {
      service(AgentService, {
        async *watchOverview() {
          yield {
            overview: create(OverviewSchema, {
              appleAccount: { state: AppleAccountState.SIGNED_IN },
              activity: { phase: Phase.IDLE },
            }),
          };
          await new Promise(() => undefined);
        },
        getSettings: () => create(GetSettingsResponseSchema, { settings: create(SettingsSchema) }),
        startSync: () => {
          syncStarted = true;
          return { started: true };
        },
      });
    });

    window.location.hash = "#/";
    render(<App transport={transport} platform={desktopLike} />);

    fireEvent.keyDown(window, { key: "k", ctrlKey: true });
    const searchBox = await screen.findByPlaceholderText("Type a page, command or action…");

    const user = userEvent.setup();
    await user.type(searchBox, "start sync");

    const syncOption = await screen.findByRole("option", { name: /Start Sync Now/ });
    await user.click(syncOption);

    expect(syncStarted).toBe(true);
  });
});
