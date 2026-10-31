import { ConnectError } from "@connectrpc/connect";
import { useQuery } from "@connectrpc/connect-query";
import {
  Breadcrumb,
  BreadcrumbButton,
  BreadcrumbDivider,
  BreadcrumbItem,
  Button,
  Dialog,
  DialogActions,
  DialogBody,
  DialogContent,
  DialogSurface,
  DialogTitle,
  Field,
  Input,
  makeStyles,
  Spinner,
  Text,
  tokens,
} from "@fluentui/react-components";
import { FolderRegular } from "@fluentui/react-icons";
import { Fragment, useState } from "react";

import { baseName, join, parentOf, segments } from "../filePaths";
import { AgentService } from "../gen/homecloud/agent/v3/agent_pb";
import { LoadError } from "./LoadError";

const useStyles = makeStyles({
  folders: {
    display: "flex",
    flexDirection: "column",
    gap: tokens.spacingVerticalXXS,
    maxHeight: "40vh",
    overflowY: "auto",
    marginTop: tokens.spacingVerticalS,
  },
  folder: { justifyContent: "flex-start" },
  muted: { color: tokens.colorNeutralForeground3 },
});

/**
 * Asks for a name: a new folder's, or a file's new one. The agent checks it;
 * its reason shows under the field and the dialog stays open.
 */
export function NameDialog({
  open,
  title,
  action,
  initial,
  onClose,
  onSubmit,
}: {
  open: boolean;
  title: string;
  action: string;
  initial: string;
  onClose: () => void;
  onSubmit: (name: string) => Promise<unknown>;
}) {
  const [name, setName] = useState(initial);
  const [error, setError] = useState<string | undefined>(undefined);
  const [pending, setPending] = useState(false);
  const blank = name.trim() === "";
  return (
    <Dialog
      open={open}
      onOpenChange={(_, d) => {
        if (!d.open) {
          onClose();
        }
      }}
    >
      <DialogSurface>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            if (blank) {
              return;
            }
            setPending(true);
            onSubmit(name.trim()).then(
              () => {
                setPending(false);
                onClose();
              },
              (err: unknown) => {
                setPending(false);
                setError(ConnectError.from(err).rawMessage);
              },
            );
          }}
        >
          <DialogBody>
            <DialogTitle>{title}</DialogTitle>
            <DialogContent>
              <Field label="Name" required validationMessage={error}>
                <Input
                  value={name}
                  autoFocus
                  onChange={(_, d) => {
                    setName(d.value);
                    setError(undefined);
                  }}
                />
              </Field>
            </DialogContent>
            <DialogActions>
              <Button appearance="secondary" onClick={onClose}>
                Cancel
              </Button>
              <Button appearance="primary" type="submit" disabled={blank || pending}>
                {action}
              </Button>
            </DialogActions>
          </DialogBody>
        </form>
      </DialogSurface>
    </Dialog>
  );
}

/**
 * Picks the folder to move items into, within the same location. The items
 * themselves cannot be entered (a folder cannot move into itself).
 */
export function MoveDialog({
  open,
  location,
  start,
  moving,
  onClose,
  onMove,
}: {
  open: boolean;
  location: string;
  start: string;
  /** The paths being moved. */
  moving: readonly string[];
  onClose: () => void;
  onMove: (destination: string) => Promise<unknown>;
}) {
  const styles = useStyles();
  const [dir, setDir] = useState(start);
  const [pending, setPending] = useState(false);
  const list = useQuery(AgentService.method.listFiles, { location, path: dir }, { enabled: open });
  const folders = (list.data?.entries ?? []).filter((e) => e.folder && !moving.includes(join(dir, e.name)));
  const parts = segments(dir);
  const here = moving.every((p) => parentOf(p) === dir);
  return (
    <Dialog
      open={open}
      onOpenChange={(_, d) => {
        if (!d.open) {
          onClose();
        }
      }}
    >
      <DialogSurface>
        <DialogBody>
          <DialogTitle>
            Move {moving.length === 1 ? `“${baseName(moving[0] ?? "")}”` : `${String(moving.length)} items`}
          </DialogTitle>
          <DialogContent>
            <Breadcrumb aria-label="Destination folder" size="small">
              <BreadcrumbItem>
                <BreadcrumbButton
                  current={parts.length === 0}
                  onClick={() => {
                    setDir("");
                  }}
                >
                  {location}
                </BreadcrumbButton>
              </BreadcrumbItem>
              {parts.map((p, i) => (
                <Fragment key={parts.slice(0, i + 1).join("/")}>
                  <BreadcrumbDivider />
                  <BreadcrumbItem>
                    <BreadcrumbButton
                      current={i === parts.length - 1}
                      onClick={() => {
                        setDir(parts.slice(0, i + 1).join("/"));
                      }}
                    >
                      {p}
                    </BreadcrumbButton>
                  </BreadcrumbItem>
                </Fragment>
              ))}
            </Breadcrumb>
            <div className={styles.folders} role="list" aria-label="Folders">
              {list.isPending && <Spinner size="tiny" />}
              {list.isError && <LoadError what="this folder" error={list.error} onRetry={() => void list.refetch()} />}
              {list.isSuccess && folders.length === 0 && <Text className={styles.muted}>No folders here.</Text>}
              {folders.map((f) => (
                <div role="listitem" key={f.name}>
                  <Button
                    appearance="subtle"
                    className={styles.folder}
                    icon={<FolderRegular />}
                    onClick={() => {
                      setDir(join(dir, f.name));
                    }}
                  >
                    {f.name}
                  </Button>
                </div>
              ))}
            </div>
          </DialogContent>
          <DialogActions>
            <Button appearance="secondary" onClick={onClose}>
              Cancel
            </Button>
            <Button
              appearance="primary"
              disabled={here || pending}
              onClick={() => {
                setPending(true);
                onMove(dir).then(
                  () => {
                    setPending(false);
                    onClose();
                  },
                  () => {
                    // The caller reports the error.
                    setPending(false);
                  },
                );
              }}
            >
              Move here
            </Button>
          </DialogActions>
        </DialogBody>
      </DialogSurface>
    </Dialog>
  );
}

/** Confirms moving items to the trash. */
export function TrashDialog({
  open,
  names,
  retentionDays,
  onClose,
  onTrash,
}: {
  open: boolean;
  names: readonly string[];
  retentionDays: number;
  onClose: () => void;
  onTrash: () => Promise<unknown>;
}) {
  const [pending, setPending] = useState(false);
  const what = names.length === 1 ? `“${names[0] ?? ""}”` : `${String(names.length)} items`;
  return (
    <Dialog
      open={open}
      onOpenChange={(_, d) => {
        if (!d.open) {
          onClose();
        }
      }}
    >
      <DialogSurface>
        <DialogBody>
          <DialogTitle>Delete {what}?</DialogTitle>
          <DialogContent>
            {names.length === 1 ? "It moves" : "They move"} to this location&apos;s trash, where you can restore{" "}
            {names.length === 1 ? "it" : "them"} for {String(retentionDays)} days.
          </DialogContent>
          <DialogActions>
            <Button appearance="secondary" onClick={onClose}>
              Cancel
            </Button>
            <Button
              appearance="primary"
              disabled={pending}
              onClick={() => {
                setPending(true);
                onTrash().then(
                  () => {
                    setPending(false);
                    onClose();
                  },
                  () => {
                    setPending(false);
                  },
                );
              }}
            >
              Delete
            </Button>
          </DialogActions>
        </DialogBody>
      </DialogSurface>
    </Dialog>
  );
}
