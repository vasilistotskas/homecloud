import { create } from "@bufbuild/protobuf";
import { Code, ConnectError, createClient } from "@connectrpc/connect";
import { useTransport } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useState, type ReactNode } from "react";

import { AgentService, WatchOverviewRequestSchema } from "../gen/homecloud/agent/v1/agent_pb";
import { usePlatform } from "../app/platformContext";
import { OverviewContext, type OverviewState } from "./overviewContext";

const maxBackoffMs = 30_000;

function backoff(attempt: number): number {
  return Math.min(1000 * 2 ** attempt, maxBackoffMs);
}

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, ms);
    signal.addEventListener(
      "abort",
      () => {
        clearTimeout(timer);
        resolve();
      },
      { once: true },
    );
  });
}

/** Keeps one WatchOverview stream open for the whole app and reconnects with backoff. */
export function OverviewProvider({ children }: { children: ReactNode }) {
  const transport = useTransport();
  const platform = usePlatform();
  const queryClient = useQueryClient();
  const [generation, setGeneration] = useState(0);
  const [state, setState] = useState<Omit<OverviewState, "reconnect">>({
    overview: undefined,
    receivedAt: undefined,
    connection: "connecting",
  });

  useEffect(() => {
    const client = createClient(AgentService, transport);
    const abort = new AbortController();
    // A function, not the property, so the compiler does not narrow it across awaits.
    const stopped = () => abort.signal.aborted;
    void (async () => {
      let attempt = 0;
      while (!stopped()) {
        let first = true;
        try {
          for await (const res of client.watchOverview(create(WatchOverviewRequestSchema), {
            signal: abort.signal,
          })) {
            attempt = 0;
            if (first) {
              // Data fetched while the agent was unreachable may be stale or failed.
              first = false;
              void queryClient.invalidateQueries();
            }
            setState({ overview: res.overview, receivedAt: new Date(), connection: "live" });
          }
        } catch (err) {
          if (stopped()) {
            return;
          }
          if (ConnectError.from(err).code === Code.Unauthenticated) {
            if (platform.kind === "browser") {
              setState((s) => ({ ...s, connection: "unauthenticated" }));
              return;
            }
            // The desktop app re-reads the agent's new token and keeps trying.
            platform.onUnauthenticated();
          }
        }
        setState((s) => ({ ...s, connection: "reconnecting" }));
        await sleep(backoff(attempt++), abort.signal);
      }
    })();
    return () => {
      abort.abort();
    };
  }, [transport, platform, queryClient, generation]);

  const value: OverviewState = {
    ...state,
    reconnect: () => {
      setState((s) => ({ ...s, connection: "connecting" }));
      setGeneration((g) => g + 1);
    },
  };
  return <OverviewContext value={value}>{children}</OverviewContext>;
}
