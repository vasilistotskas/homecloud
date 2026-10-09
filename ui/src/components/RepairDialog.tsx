import {
  Button,
  Dialog,
  DialogActions,
  DialogBody,
  DialogContent,
  DialogSurface,
  DialogTitle,
  DialogTrigger,
  Text,
} from "@fluentui/react-components";
import { useState } from "react";

/**
 * Confirms a repair of a quarantined asset: its failing files are downloaded
 * again from iCloud, and the damaged ones are kept in the archive.
 */
export function RepairDialog({
  filename,
  pending,
  onRepair,
}: {
  filename: string;
  pending: boolean;
  onRepair: () => Promise<unknown>;
}) {
  const [open, setOpen] = useState(false);
  return (
    <Dialog
      open={open}
      onOpenChange={(_, d) => {
        setOpen(d.open);
      }}
    >
      <DialogTrigger disableButtonEnhancement>
        <Button>Repair…</Button>
      </DialogTrigger>
      <DialogSurface>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            void onRepair().then(
              () => {
                setOpen(false);
              },
              () => {
                // The caller reports the error; the dialog stays open to retry.
              },
            );
          }}
        >
          <DialogBody>
            <DialogTitle>Download {filename} again?</DialogTitle>
            <DialogContent>
              <Text block>
                Do this when the file on disk is damaged and the copy in iCloud is fine. The next sync, which starts now
                when nothing else runs, moves the damaged file to the archive&apos;s Damaged folder, where it&apos;s
                kept, and kei downloads the file again. Every check then runs on the new file. If kei can&apos;t
                download it again, the file is put back and stays in quarantine.
              </Text>
            </DialogContent>
            <DialogActions>
              <DialogTrigger disableButtonEnhancement>
                <Button>Cancel</Button>
              </DialogTrigger>
              <Button appearance="primary" type="submit" disabled={pending}>
                Download again
              </Button>
            </DialogActions>
          </DialogBody>
        </form>
      </DialogSurface>
    </Dialog>
  );
}
