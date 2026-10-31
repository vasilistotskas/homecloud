import { create } from "@bufbuild/protobuf";
import { createRouterTransport } from "@connectrpc/connect";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  AgentService,
  ListFileLocationsResponseSchema,
  OverviewSchema,
  Phase,
} from "../gen/homecloud/agent/v3/agent_pb";
import { testPlatform } from "../test/platform";
import { App } from "./App";
import { narrowQuery } from "./breakpoints";
import { navigate } from "./router";

function agent() {
  return createRouterTransport(({ service }) => {
    service(AgentService, {
      async *watchOverview() {
        yield { overview: create(OverviewSchema, { activity: { phase: Phase.IDLE } }) };
        await new Promise(() => undefined);
      },
      listFileLocations: () => create(ListFileLocationsResponseSchema, {}),
    });
  });
}

/** A phone-sized window: only the narrow query matches. */
function narrowWindow() {
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: query === narrowQuery,
    media: query,
    onchange: null,
    addEventListener: () => undefined,
    removeEventListener: () => undefined,
    dispatchEvent: () => false,
  }));
}

describe("Shell on a phone", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("shows a top bar whose menu opens the navigation over the page and closes on a choice", async () => {
    narrowWindow();
    navigate("/files");
    render(<App transport={agent()} platform={testPlatform()} />);
    expect(await screen.findByRole("heading", { name: "Files" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Activity" })).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Menu" }));
    const activity = await screen.findByText("Activity");
    await userEvent.click(activity);
    expect(window.location.hash).toBe("#/activity");
    await waitFor(() => {
      expect(screen.queryByText("Quarantine")).not.toBeInTheDocument();
    });
  });
});
