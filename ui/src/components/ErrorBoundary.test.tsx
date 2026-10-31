import { FluentProvider, webLightTheme } from "@fluentui/react-components";
import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { PlatformContext, type Platform } from "../app/platformContext";
import { ErrorBoundary } from "./ErrorBoundary";
import { testPlatform } from "../test/platform";

function Broken(): never {
  throw new Error("render failed");
}

describe("ErrorBoundary", () => {
  it("shows a reload screen instead of a blank page", () => {
    const platform: Platform = testPlatform();
    // React reports the caught error on the console; main.tsx logs it in the app.
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => undefined);
    render(
      <FluentProvider theme={webLightTheme}>
        <PlatformContext value={platform}>
          <ErrorBoundary>
            <Broken />
          </ErrorBoundary>
        </PlatformContext>
      </FluentProvider>,
    );
    consoleError.mockRestore();
    expect(screen.getByRole("heading", { name: "This page stopped working" })).toBeInTheDocument();
    expect(screen.getByText(/recorded in the browser's console/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reload" })).toBeInTheDocument();
  });
});
