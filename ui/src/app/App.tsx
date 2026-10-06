import { Code, ConnectError, type Transport } from "@connectrpc/connect";
import { TransportProvider } from "@connectrpc/connect-query";
import { FluentProvider, makeStaticStyles, makeStyles, tokens } from "@fluentui/react-components";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";

import { OverviewProvider } from "../api/overview";
import { PlatformContext, type Platform } from "./platformContext";
import { AppRoutes } from "./AppRoutes";
import { ThemeStateProvider } from "./theme";

// The browser's default 8 px body margin would make the page taller than the
// window, so the whole app (sidebar included) would scroll by a few pixels.
const useGlobalStyles = makeStaticStyles({ body: { margin: 0 } });

/** Errors worth one more try; anything else is shown at once (a retry would fail the same way). */
const transientCodes: ReadonlySet<Code> = new Set([
  Code.Unavailable,
  Code.DeadlineExceeded,
  Code.Aborted,
  Code.ResourceExhausted,
]);

function retryTransient(failureCount: number, error: unknown): boolean {
  return failureCount < 1 && transientCodes.has(ConnectError.from(error).code);
}

const useStyles = makeStyles({
  root: { minHeight: "100vh", backgroundColor: tokens.colorNeutralBackground3 },
});

/** The app, talking to the agent through transport, in the given platform. */
export function App({ transport, platform }: { transport: Transport; platform: Platform }) {
  useGlobalStyles();
  const styles = useStyles();
  const [queryClient] = useState(
    () => new QueryClient({ defaultOptions: { queries: { retry: retryTransient, refetchOnWindowFocus: true } } }),
  );
  return (
    <ThemeStateProvider>
      {(theme) => (
        // The root styles lay out the page; portals (toasts, dialogs, menus) only
        // need the theme, or each would cover the window with the page background.
        <FluentProvider theme={theme} className={styles.root} applyStylesToPortals={false}>
          <PlatformContext value={platform}>
            <TransportProvider transport={transport}>
              <QueryClientProvider client={queryClient}>
                <OverviewProvider>
                  <AppRoutes />
                </OverviewProvider>
              </QueryClientProvider>
            </TransportProvider>
          </PlatformContext>
        </FluentProvider>
      )}
    </ThemeStateProvider>
  );
}
