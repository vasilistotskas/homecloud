import { isTauri } from "@tauri-apps/api/core";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { DesktopTransport, desktopPlatform } from "./api/desktop";
import { browserTransport } from "./api/transport";
import { App } from "./app/App";
import type { Platform } from "./app/platformContext";

const root = document.getElementById("root");
if (root === null) {
  throw new Error("index.html has no #root element");
}

function environment() {
  if (isTauri()) {
    const transport = new DesktopTransport();
    return { transport, platform: desktopPlatform(transport) };
  }
  const platform: Platform = { kind: "browser" };
  return { transport: browserTransport(), platform };
}

const { transport, platform } = environment();
createRoot(root).render(
  <StrictMode>
    <App transport={transport} platform={platform} />
  </StrictMode>,
);
