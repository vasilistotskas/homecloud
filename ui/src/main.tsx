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
  const platform: Platform = { kind: "browser", mediaUrl: (path) => path };
  return { transport: browserTransport(), platform };
}

const { transport, platform } = await environment();
createRoot(root).render(
  <StrictMode>
    <App transport={transport} platform={platform} />
  </StrictMode>,
);
