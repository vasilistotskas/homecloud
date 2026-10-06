import { Button, MessageBar, MessageBarActions, MessageBarBody, MessageBarTitle } from "@fluentui/react-components";
import { ConnectError } from "@connectrpc/connect";

/**
 * Shows why data could not be loaded, in the agent's own words, instead of an
 * empty list that would look like "nothing here".
 */
export function LoadError({ what, error, onRetry }: { what: string; error: unknown; onRetry: () => void }) {
  return (
    <MessageBar intent="error">
      <MessageBarBody>
        <MessageBarTitle>Couldn&apos;t load {what}</MessageBarTitle>
        {ConnectError.from(error).rawMessage}
      </MessageBarBody>
      <MessageBarActions>
        <Button onClick={onRetry}>Try again</Button>
      </MessageBarActions>
    </MessageBar>
  );
}
