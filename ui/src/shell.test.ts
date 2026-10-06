import { describe, expect, it } from "vitest";

import { shellCommand } from "./shell";

describe("shellCommand", () => {
  it("single-quotes every word for PowerShell and runs the program through the call operator", () => {
    expect(
      shellCommand("windows", String.raw`C:\Program Files\homecloud\homecloud.exe`, [
        "icloud",
        "login",
        "--config",
        String.raw`C:\Users\O'Brien\AppData\Roaming\homecloud\config.toml`,
      ]),
    ).toBe(
      String.raw`& 'C:\Program Files\homecloud\homecloud.exe' 'icloud' 'login' '--config' 'C:\Users\O''Brien\AppData\Roaming\homecloud\config.toml'`,
    );
  });

  it("keeps PowerShell's special characters literal", () => {
    expect(shellCommand("windows", String.raw`C:\a\b.exe`, ["$env:HOME `n ; & (x)"])).toBe(
      String.raw`& 'C:\a\b.exe' '$env:HOME ` + "`n ; & (x)'",
    );
  });

  it("single-quotes every word for POSIX shells", () => {
    expect(shellCommand("linux", "/usr/local/bin/homecloud", ["--config", "/home/me/it's $HOME `x`.toml"])).toBe(
      String.raw`'/usr/local/bin/homecloud' '--config' '/home/me/it'\''s $HOME ` + "`x`.toml'",
    );
  });
});
