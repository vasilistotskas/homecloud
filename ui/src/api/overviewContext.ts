import { createContext, use } from "react";

import type { Overview } from "../gen/homecloud/agent/v1/agent_pb";

/** Connection to the agent's overview stream. */
export type Connection = "connecting" | "live" | "reconnecting" | "unauthenticated";

export interface OverviewState {
  /** The latest overview; kept while reconnecting so the UI can show its age. */
  overview: Overview | undefined;
  /** When the latest overview arrived. */
  receivedAt: Date | undefined;
  connection: Connection;
  /** Restarts the stream, e.g. after signing in. */
  reconnect: () => void;
}

export const OverviewContext = createContext<OverviewState | undefined>(undefined);

export function useOverview(): OverviewState {
  const ctx = use(OverviewContext);
  if (ctx === undefined) {
    throw new Error("useOverview must be used inside OverviewProvider");
  }
  return ctx;
}
