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

import { useNotify } from "./toast";

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

/** A dialog that explains a terminal command and lets the user copy it. */
export function CommandDialog({
  trigger,
  title,
  intro,
  command,
  after,
}: {
  trigger: string;
  title: string;
  intro: ReactNode;
  /** Undefined while the settings that make up the command load. */
  command: string | undefined;
  after: ReactNode;
}) {
  const styles = useStyles();
  const notify = useNotify();
  return (
    <Dialog>
      <DialogTrigger disableButtonEnhancement>
        <Button appearance="primary">{trigger}</Button>
      </DialogTrigger>
      <DialogSurface>
        <DialogBody>
          <DialogTitle>{title}</DialogTitle>
          <DialogContent>
            <Text block>{intro}</Text>
            {command === undefined ? (
              <Spinner size="tiny" />
            ) : (
              <div className={styles.command}>
                <span style={{ flex: 1 }}>{command}</span>
                <Button
                  icon={<CopyRegular />}
                  aria-label="Copy command"
                  onClick={() => {
                    navigator.clipboard.writeText(command).then(
                      () => {
                        notify("success", "Command copied");
                      },
                      () => {
                        notify("error", "Couldn't copy the command");
                      },
                    );
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
