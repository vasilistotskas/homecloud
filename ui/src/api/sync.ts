import { useMutation } from "@connectrpc/connect-query";

import { useNotify } from "../components/toast";
import { AgentService, Phase } from "../gen/homecloud/agent/v3/agent_pb";
import { useOverview } from "./overviewContext";

/** Phases of a sync cycle; the others are idle time or maintenance jobs. */
export const cyclePhases: ReadonlySet<Phase> = new Set([
  Phase.DOWNLOADING,
  Phase.INGESTING,
  Phase.VERIFYING,
  Phase.PREPARING_PREVIEWS,
  Phase.UPDATING_VIEWER,
]);

/**
 * Starting a sync: whether one can start now (the agent is connected and no
 * cycle runs), whether it is running, and the request, which tells the user
 * when the agent queued it behind a job or already had one queued.
 */
export function useStartSync() {
  const { overview, connection } = useOverview();
  const notify = useNotify();
  const phase = overview?.activity?.phase ?? Phase.UNSPECIFIED;
  const syncing = cyclePhases.has(phase);
  const mutation = useMutation(AgentService.method.startSync, {
    onSuccess: (res) => {
      if (!res.started) {
        notify("info", "A sync is already running or queued");
      } else if (phase !== Phase.IDLE) {
        notify("info", "Sync queued: it starts as soon as the current job finishes");
      }
    },
    onError: (err) => {
      notify("error", "Couldn't start a sync", err.message);
    },
  });
  return {
    syncing,
    canStart: connection === "live" && !syncing && !mutation.isPending,
    start: () => {
      mutation.mutate({});
    },
  };
}
