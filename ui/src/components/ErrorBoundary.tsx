import { Button, Card, makeStyles, Text, Title3, tokens } from "@fluentui/react-components";
import { Component, type ReactNode } from "react";

import { usePlatform } from "../app/platformContext";

const useStyles = makeStyles({
  root: {
    minHeight: "100vh",
    display: "flex",
    alignItems: "center",
    justifyContent: "center",
    padding: `${tokens.spacingVerticalXXXL} ${tokens.spacingHorizontalL}`,
    boxSizing: "border-box",
  },
  card: { width: "min(480px, 100%)", padding: tokens.spacingHorizontalXXXL, gap: tokens.spacingVerticalL },
});

/**
 * Shows a reload screen instead of a blank window when the page crashes while
 * rendering. React reports the error to the root's onCaughtError (main.tsx),
 * which logs it. React offers error boundaries only as class components.
 */
export class ErrorBoundary extends Component<{ children: ReactNode }, { failed: boolean }> {
  override state = { failed: false };

  static getDerivedStateFromError(): { failed: boolean } {
    return { failed: true };
  }

  override render(): ReactNode {
    return this.state.failed ? <Crashed /> : this.props.children;
  }
}

function Crashed() {
  const styles = useStyles();
  const platform = usePlatform();
  return (
    <div className={styles.root}>
      <Card className={styles.card}>
        <Title3 as="h1">This page stopped working</Title3>
        <Text>
          Your archive is not affected: the agent keeps running. The error was recorded{" "}
          {platform.kind === "desktop" ? "in the app's log" : "in the browser's console"}.
        </Text>
        <Button
          appearance="primary"
          onClick={() => {
            window.location.reload();
          }}
        >
          Reload
        </Button>
      </Card>
    </div>
  );
}
