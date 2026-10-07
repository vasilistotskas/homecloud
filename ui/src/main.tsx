import { isTauri } from "@tauri-apps/api/core";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { DesktopTransport, desktopConnection, desktopPlatform } from "./api/desktop";
import { browserTransport } from "./api/transport";
import { App } from "./app/App";
import type { Platform } from "./app/platformContext";

const root = document.getElementById("root");
if (root === null) {
  throw new Error("index.html has no #root element");
}

async function environment() {
  if (isTauri()) {
    // Which agent to use decides what the app offers (this PC's file
    // actions, or a paired homecloud), so it is known before the first render.
    const transport = new DesktopTransport(await desktopConnection());
    return { transport, platform: desktopPlatform(transport) };
  }
  const platform: Platform = {
    kind: "browser",
    mediaUrl: (path) => path,
    logError: (message) => {
      console.error(message);
    },
  };
  return { transport: browserTransport(), platform };
}

/** An error as one log line: its stack when it has one. */
function describe(err: unknown): string {
  return err instanceof Error ? (err.stack ?? `${err.name}: ${err.message}`) : String(err);
}

const { transport, platform } = await environment();
// Errors nothing else handles. React reports render errors through the root
// options below instead (the page shows a reload screen for those).
window.addEventListener("error", (e) => {
  platform.logError(`uncaught error: ${describe(e.error ?? e.message)}`);
});
window.addEventListener("unhandledrejection", (e) => {
  platform.logError(`unhandled rejection: ${describe(e.reason)}`);
});
createRoot(root, {
  onCaughtError: (err, info) => {
    platform.logError(`page crashed: ${describe(err)}${info.componentStack ?? ""}`);
  },
  onUncaughtError: (err, info) => {
    platform.logError(`page crashed: ${describe(err)}${info.componentStack ?? ""}`);
  },
}).render(
  <StrictMode>
    <App transport={transport} platform={platform} />
  </StrictMode>,
);
