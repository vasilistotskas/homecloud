import { FluentProvider, webLightTheme } from "@fluentui/react-components";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { ReleaseDialog } from "./ReleaseDialog";

function setup(onRelease = vi.fn(() => Promise.resolve())) {
  render(
    <FluentProvider theme={webLightTheme}>
      <ReleaseDialog filename="IMG_3103.MOV" pending={false} onRelease={onRelease} />
    </FluentProvider>,
  );
  return { user: userEvent.setup(), onRelease };
}

describe("ReleaseDialog", () => {
  it("requires a non-blank reason", async () => {
    const { user, onRelease } = setup();
    await user.click(screen.getByRole("button", { name: "Release…" }));
    const release = screen.getByRole("button", { name: "Release" });
    expect(release).toBeDisabled();

    const reason = screen.getByRole("textbox", { name: /reason/i });
    await user.type(reason, "   ");
    expect(release).toBeDisabled();

    await user.type(reason, "opened it, plays fine  ");
    expect(release).toBeEnabled();
    await user.click(release);
    expect(onRelease).toHaveBeenCalledWith("opened it, plays fine");
  });

  it("stays open when the release fails", async () => {
    const { user } = setup(vi.fn(() => Promise.reject(new Error("agent unreachable"))));
    await user.click(screen.getByRole("button", { name: "Release…" }));
    await user.type(screen.getByRole("textbox", { name: /reason/i }), "checked");
    await user.click(screen.getByRole("button", { name: "Release" }));
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });
});
