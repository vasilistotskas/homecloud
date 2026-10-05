import type { Transport } from "@connectrpc/connect";
import { TransportProvider } from "@connectrpc/connect-query";
import { FluentProvider, makeStyles, tokens } from "@fluentui/react-components";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { useState } from "react";

import { OverviewProvider } from "../api/overview";
import { createAppRouter } from "./router";
import { ThemeStateProvider } from "./theme";

const useStyles = makeStyles({
  root: { minHeight: "100vh", backgroundColor: tokens.colorNeutralBackground3 },
});

/** The app, talking to the agent through transport. */
export function App({ transport }: { transport: Transport }) {
  const styles = useStyles();
  const [router] = useState(createAppRouter);
  const [queryClient] = useState(
    () => new QueryClient({ defaultOptions: { queries: { retry: 1, refetchOnWindowFocus: true } } }),
  );
  return (
    <ThemeStateProvider>
      {(theme) => (
        <FluentProvider theme={theme} className={styles.root}>
          <TransportProvider transport={transport}>
            <QueryClientProvider client={queryClient}>
              <OverviewProvider>
                <RouterProvider router={router} />
              </OverviewProvider>
            </QueryClientProvider>
          </TransportProvider>
        </FluentProvider>
      )}
    </ThemeStateProvider>
  );
}
