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

const browser: Platform = {
  kind: "browser",
  mediaUrl: (path) => "http://agent" + path,
  logError: (m) => expect.unreachable(m),
};

function agent(phase: Phase, onStartSync: () => void = () => undefined) {
  return createRouterTransport(({ service }) => {
    service(AgentService, {
      async *watchOverview() {
        yield {
          overview: create(OverviewSchema, {
            appleAccount: { state: AppleAccountState.SIGNED_IN },
            activity: { phase },
          }),
        };
        await new Promise(() => undefined);
      },
      getSettings: () => create(GetSettingsResponseSchema, { settings: create(SettingsSchema) }),
      listRuns: () => create(ListRunsResponseSchema),
      startSync: () => {
        onStartSync();
        return { started: true };
      },
    });
  });
}

/**
 * Opens the palette with Ctrl+K once the agent is connected, returning its
 * input. Fluent's Dialog focuses the input on open in a browser; jsdom has no
 * layout, so Tabster finds nothing focusable there and the test focuses it.
 */
async function openPalette(): Promise<HTMLElement> {
  await screen.findByText("Agent connected");
  fireEvent.keyDown(window, { key: "k", ctrlKey: true });
  const input = await screen.findByRole("combobox", { name: "Command" });
  input.focus();
  return input;
}

describe("CommandPalette", () => {
  it("opens with Ctrl+K focused on the search and goes to an Activity tab with Enter", async () => {
    window.location.hash = "#/";
    render(<App transport={agent(Phase.IDLE)} platform={browser} />);
    await openPalette();
    await userEvent.setup().keyboard("integrity{Enter}");
    expect(window.location.hash).toBe("#/activity?tab=integrity");
    expect(screen.queryByRole("combobox", { name: "Command" })).not.toBeInTheDocument();
  });

  it("lists every page and every Activity tab, and moves the active option with the arrow keys", async () => {
    window.location.hash = "#/";
    render(<App transport={agent(Phase.IDLE)} platform={browser} />);
    const input = await openPalette();
    for (const name of ["Overview", "Library", "Activity", "Quarantine", "Settings", "Activity: Records backups"]) {
      expect(screen.getByRole("option", { name })).toBeInTheDocument();
    }

    const user = userEvent.setup();
    expect(input).toHaveAttribute("aria-activedescendant", screen.getByRole("option", { name: "Overview" }).id);
    await user.keyboard("{ArrowDown}");
    const library = screen.getByRole("option", { name: "Library" });
    expect(input).toHaveAttribute("aria-activedescendant", library.id);
    expect(library).toHaveAttribute("aria-selected", "true");
    await user.keyboard("{ArrowUp}{ArrowUp}");
    expect(screen.getByRole("option", { selected: true })).toBe(screen.getAllByRole("option").at(-1));
  });

  it("starts a sync when none runs and offers none while a sync runs", async () => {
    let started = 0;
    window.location.hash = "#/";
    const { unmount } = render(
      <App
        transport={agent(Phase.IDLE, () => {
          started++;
        })}
        platform={browser}
      />,
    );
    await openPalette();
    await userEvent.setup().click(screen.getByRole("option", { name: "Sync now" }));
    await expect.poll(() => started).toBe(1);
    unmount();

    render(<App transport={agent(Phase.DOWNLOADING)} platform={browser} />);
    await openPalette();
    expect(screen.queryByRole("option", { name: "Sync now" })).not.toBeInTheDocument();
  });

  it("starts empty every time it opens", async () => {
    window.location.hash = "#/";
    render(<App transport={agent(Phase.IDLE)} platform={browser} />);
    const user = userEvent.setup();
    await openPalette();
    await user.keyboard("zzz");
    expect(screen.getByText("No page or command matches.")).toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("combobox", { name: "Command" })).not.toBeInTheDocument();

    expect(await openPalette()).toHaveValue("");
  });
});
