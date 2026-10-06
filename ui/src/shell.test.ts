import { afterEach, describe, expect, it, vi } from "vitest";

import { shellCommand } from "./shell";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("shellCommand", () => {
  it("uses PowerShell's call operator on Windows", () => {
    vi.stubGlobal("navigator", { userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64)" });
    expect(
      shellCommand(String.raw`C:\Program Files\homecloud\homecloud.exe`, [
        "icloud",
        "login",
        "--config",
        String.raw`C:\Users\me\AppData\Roaming\homecloud\config.toml`,
      ]),
    ).toBe(
      String.raw`& "C:\Program Files\homecloud\homecloud.exe" icloud login --config C:\Users\me\AppData\Roaming\homecloud\config.toml`,
    );
  });

  it("quotes paths with spaces elsewhere", () => {
    vi.stubGlobal("navigator", { userAgent: "Mozilla/5.0 (X11; Linux x86_64)" });
    expect(shellCommand("/usr/local/bin/homecloud", ["icloud", "login", "--config", "/home/me/my config.toml"])).toBe(
      `"/usr/local/bin/homecloud" icloud login --config "/home/me/my config.toml"`,
    );
  });
});
