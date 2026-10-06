/**
 * A command line that runs `binary` with `args` in the user's terminal. The
 * app only talks to the agent on this computer, so the browser's platform is
 * the agent's. PowerShell, Windows' default terminal, runs a quoted path only
 * through its call operator.
 */
export function shellCommand(binary: string, args: string[]): string {
  const quote = (s: string) => (/[\s"]/.test(s) ? `"${s}"` : s);
  const line = [`"${binary}"`, ...args.map(quote)].join(" ");
  return navigator.userAgent.includes("Windows") ? `& ${line}` : line;
}
