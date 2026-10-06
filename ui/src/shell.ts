import type { Settings } from "./gen/homecloud/agent/v2/agent_pb";

/**
 * A command line that runs `binary` with `args` in the terminal of the agent's
 * operating system (`os` as GetInfo reports it). Every word is single-quoted,
 * so no character in a path is interpreted: PowerShell (Windows' terminal)
 * doubles a quote inside and runs a quoted program through its call operator;
 * POSIX shells end the quote, add an escaped one and reopen it.
 */
export function shellCommand(os: string, binary: string, args: string[]): string {
  if (os === "windows") {
    const quote = (s: string) => `'${s.replaceAll("'", "''")}'`;
    return ["&", quote(binary), ...args.map(quote)].join(" ");
  }
  const quote = (s: string) => `'${s.replaceAll("'", `'\\''`)}'`;
  return [quote(binary), ...args.map(quote)].join(" ");
}

/** The terminal the shown commands are written for. */
export function terminalName(os: string): string {
  return os === "windows" ? "PowerShell" : "a terminal";
}

/** The command that signs homecloud in to iCloud for iCloud Drive. */
export function icloudLoginCommand(os: string, s: Settings): string {
  return shellCommand(os, s.cli, ["icloud", "login", "--config", s.configPath]);
}

/** The command that signs the Photos downloader (kei) in to iCloud. */
export function keiLoginCommand(os: string, s: Settings): string {
  return shellCommand(os, s.downloader?.binary ?? "", ["login", "--config", s.downloaderConfig]);
}
