import {
  Button,
  Dialog,
  DialogActions,
  DialogBody,
  DialogContent,
  DialogSurface,
  DialogTitle,
  DialogTrigger,
  Field,
  Text,
  Textarea,
} from "@fluentui/react-components";
import { useState } from "react";

/**
 * Asks for the audited reason before a quarantined asset is released. Release
 * stays disabled until the reason has non-blank text, matching the agent's rule.
 */
export function ReleaseDialog({
  filename,
  pending,
  onRelease,
}: {
  filename: string;
  pending: boolean;
  onRelease: (reason: string) => Promise<unknown>;
}) {
  const [open, setOpen] = useState(false);
  const [reason, setReason] = useState("");
  const blank = reason.trim() === "";
  return (
    <Dialog
      open={open}
      onOpenChange={(_, d) => {
        setOpen(d.open);
        if (d.open) {
          setReason("");
        }
      }}
    >
      <DialogTrigger disableButtonEnhancement>
        <Button appearance="primary">Release…</Button>
      </DialogTrigger>
      <DialogSurface>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            if (!blank) {
              void onRelease(reason.trim()).then(
                () => {
                  setOpen(false);
                },
                () => {
                  // The caller reports the error; the dialog stays open to retry.
                },
              );
            }
          }}
        >
          <DialogBody>
            <DialogTitle>Release {filename} from quarantine?</DialogTitle>
            <DialogContent>
              <Text block>
                Only do this if you&apos;ve opened the file and it&apos;s fine, or the check itself was wrong. Releasing
                doesn&apos;t mark it as good: every check runs again, and it can only become eligible for iCloud cleanup
                if they all pass.
              </Text>
              <Field label="Reason" required hint="Kept in the event log.">
                <Textarea
                  value={reason}
                  resize="vertical"
                  onChange={(_, d) => {
                    setReason(d.value);
                  }}
                />
              </Field>
            </DialogContent>
            <DialogActions>
              <DialogTrigger disableButtonEnhancement>
                <Button>Cancel</Button>
              </DialogTrigger>
              <Button appearance="primary" type="submit" disabled={blank || pending}>
                Release
              </Button>
            </DialogActions>
          </DialogBody>
        </form>
      </DialogSurface>
    </Dialog>
  );
}
