import {
  Button,
  Dialog,
  DialogActions,
  DialogBody,
  DialogContent,
  DialogSurface,
  DialogTitle,
  DialogTrigger,
  makeStyles,
  Spinner,
  Text,
  tokens,
} from "@fluentui/react-components";
import { CopyRegular } from "@fluentui/react-icons";
import type { ReactNode } from "react";

import { LoadError } from "./LoadError";
import { useCopy } from "./useCopy";

const useStyles = makeStyles({
  command: {
    display: "flex",
    alignItems: "center",
    gap: tokens.spacingHorizontalS,
    padding: tokens.spacingHorizontalS,
    borderRadius: tokens.borderRadiusMedium,
    backgroundColor: tokens.colorNeutralBackground3,
    fontFamily: tokens.fontFamilyMonospace,
    wordBreak: "break-all",
  },
  muted: { color: tokens.colorNeutralForeground3 },
});

/** What a CommandDialog shows: the command, or why it is not available yet. */
export type CommandState =
  { kind: "ready"; command: string } | { kind: "loading" } | { kind: "error"; error: unknown; retry: () => void };

/** A dialog that explains a terminal command and lets the user copy it. */
export function CommandDialog({
  trigger,
  triggerLabel,
  title,
  intro,
  command,
  after,
}: {
  trigger: string;
  /** The trigger's accessible name, when several dialogs share a trigger text. */
  triggerLabel: string;
  title: string;
  intro: ReactNode;
  command: CommandState;
  after: ReactNode;
}) {
  const styles = useStyles();
  const copy = useCopy();
  return (
    <Dialog>
      <DialogTrigger disableButtonEnhancement>
        <Button appearance="primary" aria-label={triggerLabel}>
          {trigger}
        </Button>
      </DialogTrigger>
      <DialogSurface>
        <DialogBody>
          <DialogTitle>{title}</DialogTitle>
          <DialogContent>
            <Text block>{intro}</Text>
            {command.kind === "loading" && <Spinner size="tiny" label="Loading the command…" />}
            {command.kind === "error" && <LoadError what="the command" error={command.error} onRetry={command.retry} />}
            {command.kind === "ready" && (
              <div className={styles.command}>
                <span style={{ flex: 1 }}>{command.command}</span>
                <Button
                  icon={<CopyRegular />}
                  aria-label="Copy command"
                  onClick={() => {
                    copy(command.command, "Command");
                  }}
                />
              </div>
            )}
            <Text block className={styles.muted}>
              {after}
            </Text>
          </DialogContent>
          <DialogActions>
            <DialogTrigger disableButtonEnhancement>
              <Button>Close</Button>
            </DialogTrigger>
          </DialogActions>
        </DialogBody>
      </DialogSurface>
    </Dialog>
  );
}
