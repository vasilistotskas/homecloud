import { ConnectError } from "@connectrpc/connect";
import { createConnectQueryKey, useMutation, useQuery } from "@connectrpc/connect-query";
import {
  Breadcrumb,
  BreadcrumbButton,
  BreadcrumbDivider,
  BreadcrumbItem,
  Button,
  Caption1,
  Card,
  Checkbox,
  Dropdown,
  makeStyles,
  mergeClasses,
  Menu,
  MenuItem,
  MenuItemLink,
  MenuList,
  MenuPopover,
  MenuTrigger,
  MessageBar,
  MessageBarBody,
  MessageBarTitle,
  Option,
  Spinner,
  Text,
  tokens,
} from "@fluentui/react-components";
import {
  ArrowDownloadRegular,
  ArrowUploadRegular,
  DeleteRegular,
  DocumentRegular,
  FolderAddRegular,
  FolderArrowRightRegular,
  FolderRegular,
  MoreHorizontalRegular,
  OpenRegular,
  RenameRegular,
  ArrowUndoRegular,
} from "@fluentui/react-icons";
import { useQueryClient } from "@tanstack/react-query";
import { Fragment, useCallback, useEffect, useRef, useState, type DragEvent } from "react";

import { enqueueUploads, onUploadSaved } from "../api/uploads";
import { usePlatform } from "../app/platformContext";
import { navigate, useLocation } from "../app/router";
import { MoveDialog, NameDialog, TrashDialog } from "../components/FileDialogs";
import { LoadError } from "../components/LoadError";
import { Page } from "../components/Page";
import { useNotify } from "../components/toast";
import { UploadList } from "../components/UploadList";
import { baseName, join, segments } from "../filePaths";
import { formatBytes, formatWhen, toDate } from "../format";
import { AgentService, type FileEntry, type FileLocation } from "../gen/homecloud/agent/v3/agent_pb";

const useStyles = makeStyles({
  intro: { color: tokens.colorNeutralForeground3, maxWidth: "720px" },
  bar: { display: "flex", flexWrap: "wrap", alignItems: "center", gap: tokens.spacingHorizontalS },
  location: { minWidth: "160px" },
  spacer: { flexGrow: 1 },
  list: { padding: 0, gap: 0 },
  dropping: { outline: `2px dashed ${tokens.colorBrandStroke1}`, outlineOffset: "2px" },
  row: {
    display: "grid",
    gridTemplateColumns: "auto auto 1fr auto",
    alignItems: "center",
    gap: tokens.spacingHorizontalS,
    padding: `${tokens.spacingVerticalXS} ${tokens.spacingHorizontalS}`,
    borderBottom: `1px solid ${tokens.colorNeutralStroke3}`,
    minHeight: "48px",
  },
  name: {
    display: "flex",
    flexDirection: "column",
    minWidth: 0,
    alignItems: "flex-start",
    textAlign: "left",
    background: "none",
    border: "none",
    padding: 0,
    color: "inherit",
    font: "inherit",
  },
  nameButton: { cursor: "pointer" },
  nameText: { overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", maxWidth: "100%" },
  meta: { color: tokens.colorNeutralForeground3 },
  icon: { fontSize: "24px", color: tokens.colorNeutralForeground3, display: "flex" },
  folderIcon: { color: tokens.colorBrandForeground1 },
  empty: { padding: tokens.spacingVerticalL, color: tokens.colorNeutralForeground3 },
  hidden: { display: "none" },
});

type Dialog =
  | { kind: "newFolder" }
  | { kind: "rename"; path: string }
  | { kind: "move"; paths: string[] }
  | { kind: "trash"; paths: string[] };

/** The Files page: the folders named in [[files.locations]] (docs/design.md §8m). */
export function FilesPage() {
  const styles = useStyles();
  const locations = useQuery(AgentService.method.listFileLocations, {});
  const { search } = useLocation();
  const all = locations.data?.locations ?? [];
  const current = all.find((l) => l.name === search.get("loc")) ?? all[0];
  const path = search.get("path") ?? "";
  const trash = search.get("view") === "trash";

  return (
    <Page title="Files">
      {locations.isPending && <Spinner label="Loading…" />}
      {locations.isError && (
        <LoadError what="the locations" error={locations.error} onRetry={() => void locations.refetch()} />
      )}
      {locations.isSuccess && all.length === 0 && (
        <MessageBar intent="info">
          <MessageBarBody>
            <MessageBarTitle>No folders are shared here yet</MessageBarTitle>
            Name each folder this page may show in the agent&apos;s config.toml, as a [[files.locations]] entry with a
            name and a path (README, &quot;Files&quot;), then restart the agent.
          </MessageBarBody>
        </MessageBar>
      )}
      {current !== undefined && (
        <>
          <div className={styles.bar}>
            <Dropdown
              className={styles.location}
              aria-label="Location"
              value={current.name}
              selectedOptions={[current.name]}
              onOptionSelect={(_, d) => {
                if (d.optionValue !== undefined) {
                  navigate("/files", { search: { loc: d.optionValue } });
                }
              }}
            >
              {all.map((l) => (
                <Option key={l.name} value={l.name} text={l.name}>
                  {l.name}
                  {!l.available && " (not available)"}
                </Option>
              ))}
            </Dropdown>
            {current.available && <Caption1 className={styles.meta}>{formatBytes(current.freeBytes)} free</Caption1>}
          </div>
          {!current.available ? (
            <MessageBar intent="warning">
              <MessageBarBody>
                <MessageBarTitle>{current.name} is not available</MessageBarTitle>
                Its folder can&apos;t be opened: the drive may be disconnected or the share not mounted.
              </MessageBarBody>
            </MessageBar>
          ) : trash ? (
            <TrashView location={current} retention={locations.data?.trashRetentionSeconds ?? 0n} />
          ) : (
            <FolderView
              key={`${current.name}/${path}`}
              location={current}
              path={path}
              retentionDays={days(locations.data?.trashRetentionSeconds ?? 0n)}
            />
          )}
        </>
      )}
    </Page>
  );
}

function days(seconds: bigint): number {
  return Number(seconds / 86400n);
}

function go(location: string, path: string, view?: "trash"): void {
  const search: Record<string, string> = { loc: location };
  if (path !== "") {
    search.path = path;
  }
  if (view !== undefined) {
    search.view = view;
  }
  navigate("/files", { search });
}

function useInvalidateFiles(): () => Promise<void> {
  const queryClient = useQueryClient();
  return useCallback(
    () =>
      queryClient.invalidateQueries({
        queryKey: createConnectQueryKey({ schema: AgentService, cardinality: "finite" }),
      }),
    [queryClient],
  );
}

function message(err: unknown): string {
  return ConnectError.from(err).rawMessage;
}

function FolderView({
  location,
  path,
  retentionDays,
}: {
  location: FileLocation;
  path: string;
  retentionDays: number;
}) {
  const styles = useStyles();
  const notify = useNotify();
  const platform = usePlatform();
  const invalidate = useInvalidateFiles();
  const list = useQuery(AgentService.method.listFiles, { location: location.name, path });
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set());
  const [dialog, setDialog] = useState<Dialog | undefined>(undefined);
  const [dropping, setDropping] = useState(false);
  const input = useRef<HTMLInputElement>(null);
  const entries = list.data?.entries ?? [];

  // Files saved here appear without a reload.
  useEffect(
    () =>
      onUploadSaved((loc, dir) => {
        if (loc === location.name && dir === path) {
          void invalidate();
        }
      }),
    [location.name, path, invalidate],
  );

  const upload = (files: FileList | null) => {
    if (files === null || files.length === 0) {
      return;
    }
    enqueueUploads(platform.upload, [...files], location.name, path, location.uploadPath);
  };
  const createFolder = useMutation(AgentService.method.createFolder, { onSuccess: invalidate });
  const rename = useMutation(AgentService.method.renameFile, { onSuccess: invalidate });
  const move = useMutation(AgentService.method.moveFiles, {
    onSuccess: async (res) => {
      notify("success", `Moved ${String(res.moved)} ${res.moved === 1 ? "item" : "items"}`);
      setSelected(new Set());
      await invalidate();
    },
    onError: async (err) => {
      notify("error", "Couldn't move everything", message(err));
      await invalidate();
    },
  });
  const trashFiles = useMutation(AgentService.method.trashFiles, {
    onSuccess: async (res) => {
      notify("success", `Moved ${String(res.trashed)} ${res.trashed === 1 ? "item" : "items"} to the trash`);
      setSelected(new Set());
      await invalidate();
    },
    onError: async (err) => {
      notify("error", "Couldn't delete everything", message(err));
      await invalidate();
    },
  });

  const toggle = (name: string, on: boolean) => {
    const next = new Set(selected);
    if (on) {
      next.add(name);
    } else {
      next.delete(name);
    }
    setSelected(next);
  };
  const picked = entries.filter((e) => selected.has(e.name)).map((e) => join(path, e.name));
  const parts = segments(path);

  const onDrop = (e: DragEvent) => {
    e.preventDefault();
    setDropping(false);
    upload(e.dataTransfer.files);
  };

  return (
    <>
      <Text className={styles.intro}>
        Uploads resume where they stopped if the connection drops. Nothing is replaced: a file whose name is taken is
        saved as “name (2)”.
      </Text>
      <div className={styles.bar}>
        <Button
          appearance="primary"
          icon={<ArrowUploadRegular />}
          onClick={() => {
            input.current?.click();
          }}
        >
          Upload
        </Button>
        <input
          ref={input}
          type="file"
          multiple
          className={styles.hidden}
          aria-label="Files to upload"
          onChange={(e) => {
            upload(e.currentTarget.files);
            e.currentTarget.value = "";
          }}
        />
        <Button
          icon={<FolderAddRegular />}
          onClick={() => {
            setDialog({ kind: "newFolder" });
          }}
        >
          New folder
        </Button>
        {picked.length > 0 && (
          <>
            <Button
              icon={<FolderArrowRightRegular />}
              onClick={() => {
                setDialog({ kind: "move", paths: picked });
              }}
            >
              Move
            </Button>
            <Button
              icon={<DeleteRegular />}
              onClick={() => {
                setDialog({ kind: "trash", paths: picked });
              }}
            >
              Delete
            </Button>
          </>
        )}
        <span className={styles.spacer} />
        <Button
          appearance="subtle"
          icon={<DeleteRegular />}
          onClick={() => {
            go(location.name, "", "trash");
          }}
        >
          Trash
        </Button>
      </div>
      <Breadcrumb aria-label="Folder">
        <BreadcrumbItem>
          <BreadcrumbButton
            current={parts.length === 0}
            onClick={() => {
              go(location.name, "");
            }}
          >
            {location.name}
          </BreadcrumbButton>
        </BreadcrumbItem>
        {parts.map((p, i) => (
          <Fragment key={parts.slice(0, i + 1).join("/")}>
            <BreadcrumbDivider />
            <BreadcrumbItem>
              <BreadcrumbButton
                current={i === parts.length - 1}
                onClick={() => {
                  go(location.name, parts.slice(0, i + 1).join("/"));
                }}
              >
                {p}
              </BreadcrumbButton>
            </BreadcrumbItem>
          </Fragment>
        ))}
      </Breadcrumb>
      <UploadList />
      <Card
        className={mergeClasses(styles.list, dropping && styles.dropping)}
        onDragOver={(e) => {
          if (e.dataTransfer.types.includes("Files")) {
            e.preventDefault();
            setDropping(true);
          }
        }}
        onDragLeave={() => {
          setDropping(false);
        }}
        onDrop={onDrop}
      >
        {list.isPending && <Spinner className={styles.empty} size="small" />}
        {list.isError && <LoadError what="this folder" error={list.error} onRetry={() => void list.refetch()} />}
        {list.isSuccess && entries.length === 0 && (
          <Text className={styles.empty}>This folder is empty. Upload files or drop them here.</Text>
        )}
        <div role="list" aria-label="Files and folders">
          {entries.map((e) => (
            <Row
              key={e.name}
              entry={e}
              location={location.name}
              path={join(path, e.name)}
              selected={selected.has(e.name)}
              onSelect={(on) => {
                toggle(e.name, on);
              }}
              onOpen={() => {
                go(location.name, join(path, e.name));
              }}
              onAction={setDialog}
            />
          ))}
        </div>
      </Card>

      {dialog?.kind === "newFolder" && (
        <NameDialog
          open
          title="New folder"
          action="Create"
          initial=""
          onClose={() => {
            setDialog(undefined);
          }}
          onSubmit={(name) => createFolder.mutateAsync({ location: location.name, path, name })}
        />
      )}
      {dialog?.kind === "rename" && (
        <NameDialog
          open
          title="Rename"
          action="Rename"
          initial={baseName(dialog.path)}
          onClose={() => {
            setDialog(undefined);
          }}
          onSubmit={(newName) => rename.mutateAsync({ location: location.name, path: dialog.path, newName })}
        />
      )}
      {dialog?.kind === "move" && (
        <MoveDialog
          open
          location={location.name}
          start={path}
          moving={dialog.paths}
          onClose={() => {
            setDialog(undefined);
          }}
          onMove={(destination) => move.mutateAsync({ location: location.name, paths: dialog.paths, destination })}
        />
      )}
      {dialog?.kind === "trash" && (
        <TrashDialog
          open
          names={dialog.paths.map(baseName)}
          retentionDays={retentionDays}
          onClose={() => {
            setDialog(undefined);
          }}
          onTrash={() => trashFiles.mutateAsync({ location: location.name, paths: dialog.paths })}
        />
      )}
    </>
  );
}

function Row({
  entry,
  location,
  path,
  selected,
  onSelect,
  onOpen,
  onAction,
}: {
  entry: FileEntry;
  location: string;
  path: string;
  selected: boolean;
  onSelect: (on: boolean) => void;
  onOpen: () => void;
  onAction: (d: Dialog) => void;
}) {
  const styles = useStyles();
  const modified = toDate(entry.modifiedAt);
  const meta = [entry.folder ? "Folder" : formatBytes(entry.sizeBytes), modified && formatWhen(modified)]
    .filter(Boolean)
    .join(" · ");
  const name = (
    <>
      <Text className={styles.nameText}>{entry.name}</Text>
      <Caption1 className={styles.meta}>{meta}</Caption1>
    </>
  );
  return (
    <div role="listitem" className={styles.row}>
      <Checkbox
        checked={selected}
        aria-label={`Select ${entry.name}`}
        onChange={(_, d) => {
          onSelect(d.checked === true);
        }}
      />
      <span className={mergeClasses(styles.icon, entry.folder && styles.folderIcon)} aria-hidden>
        {entry.folder ? <FolderRegular /> : <DocumentRegular />}
      </span>
      {entry.folder ? (
        <button type="button" className={mergeClasses(styles.name, styles.nameButton)} onClick={onOpen}>
          {name}
        </button>
      ) : (
        <span className={styles.name}>{name}</span>
      )}
      <RowMenu entry={entry} location={location} path={path} onAction={onAction} />
    </div>
  );
}

/**
 * A file or folder's actions. A file's download and open links are signed by
 * the agent when the menu opens, so they are real links the phone's browser
 * follows on the tap itself.
 */
function RowMenu({
  entry,
  location,
  path,
  onAction,
}: {
  entry: FileEntry;
  location: string;
  path: string;
  onAction: (d: Dialog) => void;
}) {
  const platform = usePlatform();
  const [open, setOpen] = useState(false);
  const file = !entry.folder;
  const download = useQuery(AgentService.method.getFileLink, { location, path }, { enabled: open && file });
  const view = useQuery(
    AgentService.method.getFileLink,
    { location, path, inline: true },
    // The desktop app has no browser tab to show a file in.
    { enabled: open && file && platform.kind === "browser" },
  );
  return (
    <Menu
      open={open}
      onOpenChange={(_, d) => {
        setOpen(d.open);
      }}
    >
      <MenuTrigger disableButtonEnhancement>
        <Button appearance="subtle" icon={<MoreHorizontalRegular />} aria-label={`Actions for ${entry.name}`} />
      </MenuTrigger>
      <MenuPopover>
        <MenuList>
          {file && (
            <MenuItemLink
              icon={<ArrowDownloadRegular />}
              href={download.data === undefined ? "" : platform.mediaUrl(download.data.url)}
              disabled={download.data === undefined}
            >
              Download
            </MenuItemLink>
          )}
          {file && platform.kind === "browser" && (
            <MenuItemLink
              icon={<OpenRegular />}
              href={view.data === undefined ? "" : platform.mediaUrl(view.data.url)}
              target="_blank"
              rel="noopener"
              disabled={view.data === undefined}
            >
              Open
            </MenuItemLink>
          )}
          <MenuItem
            icon={<RenameRegular />}
            onClick={() => {
              onAction({ kind: "rename", path });
            }}
          >
            Rename
          </MenuItem>
          <MenuItem
            icon={<FolderArrowRightRegular />}
            onClick={() => {
              onAction({ kind: "move", paths: [path] });
            }}
          >
            Move
          </MenuItem>
          <MenuItem
            icon={<DeleteRegular />}
            onClick={() => {
              onAction({ kind: "trash", paths: [path] });
            }}
          >
            Delete
          </MenuItem>
        </MenuList>
      </MenuPopover>
    </Menu>
  );
}

function TrashView({ location, retention }: { location: FileLocation; retention: bigint }) {
  const styles = useStyles();
  const notify = useNotify();
  const invalidate = useInvalidateFiles();
  const list = useQuery(AgentService.method.listTrash, { location: location.name });
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set());
  const restore = useMutation(AgentService.method.restoreFiles, {
    onSuccess: async (res) => {
      notify(
        "success",
        `Restored ${String(res.paths.length)} ${res.paths.length === 1 ? "item" : "items"}`,
        res.paths.join(", "),
      );
      setSelected(new Set());
      await invalidate();
    },
    onError: async (err) => {
      notify("error", "Couldn't restore everything", message(err));
      await invalidate();
    },
  });
  const items = list.data?.items ?? [];
  return (
    <>
      <Text className={styles.intro}>
        Deleted files and folders stay here for {String(days(retention))} days, then they are removed for good.
        Restoring puts them back where they were.
      </Text>
      <div className={styles.bar}>
        <Button
          icon={<FolderRegular />}
          onClick={() => {
            go(location.name, "");
          }}
        >
          Back to {location.name}
        </Button>
        {selected.size > 0 && (
          <Button
            appearance="primary"
            icon={<ArrowUndoRegular />}
            disabled={restore.isPending}
            onClick={() => {
              restore.mutate({ location: location.name, ids: [...selected] });
            }}
          >
            Restore
          </Button>
        )}
      </div>
      <Card className={styles.list}>
        {list.isPending && <Spinner className={styles.empty} size="small" />}
        {list.isError && <LoadError what="the trash" error={list.error} onRetry={() => void list.refetch()} />}
        {list.isSuccess && items.length === 0 && <Text className={styles.empty}>The trash is empty.</Text>}
        <div role="list" aria-label="Deleted items">
          {items.map((it) => {
            const deleted = toDate(it.deletedAt);
            const meta = [it.folder ? "Folder" : formatBytes(it.sizeBytes), deleted && `deleted ${formatWhen(deleted)}`]
              .filter(Boolean)
              .join(" · ");
            return (
              <div role="listitem" key={it.id} className={styles.row}>
                <Checkbox
                  checked={selected.has(it.id)}
                  aria-label={`Select ${it.path}`}
                  onChange={(_, d) => {
                    const next = new Set(selected);
                    if (d.checked === true) {
                      next.add(it.id);
                    } else {
                      next.delete(it.id);
                    }
                    setSelected(next);
                  }}
                />
                <span className={mergeClasses(styles.icon, it.folder && styles.folderIcon)} aria-hidden>
                  {it.folder ? <FolderRegular /> : <DocumentRegular />}
                </span>
                <span className={styles.name}>
                  <Text className={styles.nameText}>{it.path}</Text>
                  <Caption1 className={styles.meta}>{meta}</Caption1>
                </span>
                <span />
              </div>
            );
          })}
        </div>
      </Card>
    </>
  );
}
