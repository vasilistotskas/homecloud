import type { Transport } from "@connectrpc/connect";
import { TransportProvider } from "@connectrpc/connect-query";
import { FluentProvider, makeStaticStyles, makeStyles, tokens } from "@fluentui/react-components";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { useState } from "react";

import { OverviewProvider } from "../api/overview";
import { PlatformContext, type Platform } from "./platformContext";
import { createAppRouter } from "./router";
import { ThemeStateProvider } from "./theme";

// The browser's default 8 px body margin would make the page taller than the
// window, so the whole app (sidebar included) would scroll by a few pixels.
const useGlobalStyles = makeStaticStyles({ body: { margin: 0 } });

const useStyles = makeStyles({
  root: { minHeight: "100vh", backgroundColor: tokens.colorNeutralBackground3 },
});

/** The app, talking to the agent through transport, in the given platform. */
export function App({ transport, platform }: { transport: Transport; platform: Platform }) {
  useGlobalStyles();
  const styles = useStyles();
  const [router] = useState(createAppRouter);
  const [queryClient] = useState(
    () => new QueryClient({ defaultOptions: { queries: { retry: 1, refetchOnWindowFocus: true } } }),
  );
  return (
    <ThemeStateProvider>
      {(theme) => (
        <FluentProvider theme={theme} className={styles.root}>
          <PlatformContext value={platform}>
            <TransportProvider transport={transport}>
              <QueryClientProvider client={queryClient}>
                <OverviewProvider>
                  <RouterProvider router={router} />
                </OverviewProvider>
              </QueryClientProvider>
            </TransportProvider>
          </PlatformContext>
        </FluentProvider>
      )}
    </ThemeStateProvider>
  );
}
