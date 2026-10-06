import { readFileSync } from "node:fs";
import { homedir } from "node:os";
import { isAbsolute, join } from "node:path";

import react from "@vitejs/plugin-react";
import type { ProxyOptions } from "vite";
import { defineConfig } from "vitest/config";

// Paths the agent serves besides the app itself: the API and signed media.
const agentPrefixes = ["/homecloud.agent.v1.AgentService", "/media/"];

// agentURL reads the running agent's address from its discovery file (the same
// per-user state directory the agent writes; see internal/platform StateDir).
function agentURL(): string {
  let dir: string;
  if (process.platform === "win32") {
    const localAppData = process.env.LOCALAPPDATA;
    if (localAppData === undefined) {
      throw new Error("LOCALAPPDATA is not set");
    }
    dir = join(localAppData, "homecloud");
  } else {
    const stateHome = process.env.XDG_STATE_HOME;
    dir = join(
      stateHome !== undefined && isAbsolute(stateHome) ? stateHome : join(homedir(), ".local", "state"),
      "homecloud",
    );
  }
  const endpoint = JSON.parse(readFileSync(join(dir, "agent.json"), "utf8")) as { url: string };
  return endpoint.url;
}

export default defineConfig(({ command }) => {
  const dev = command === "serve" && process.env.VITEST === undefined;
  const target = dev ? agentURL() : "";
  return {
    plugins: [react()],
    build: {
      target: "es2024",
      // The app is served from loopback (or packaged in the desktop shell), so one
      // bundle loads instantly; splitting would only add requests.
      chunkSizeWarningLimit: 2048,
    },
    server: dev
      ? {
          // The dev server proxies the API and media so the app stays
          // same-origin, like when the agent serves it, in the browser and in
          // `tauri dev` alike. The agent only accepts its own Origin.
          proxy: Object.fromEntries(
            agentPrefixes.map((prefix): [string, ProxyOptions] => [
              prefix,
              {
                target,
                changeOrigin: true,
                configure: (proxy) => {
                  proxy.on("proxyReq", (req) => {
                    req.setHeader("origin", target);
                  });
                },
              },
            ]),
          ),
        }
      : {},
    test: {
      environment: "jsdom",
      setupFiles: ["./src/test/setup.ts"],
      css: false,
    },
  };
});
