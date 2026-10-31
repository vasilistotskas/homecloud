import { useSyncExternalStore } from "react";
import { DetailedError, Upload } from "tus-js-client";

/**
 * Uploads to the Files page's locations over tus 1.0 (docs/design.md §8m),
 * one file at a time in the order chosen, so a phone's connection carries one
 * file at full speed and files land in that order. An upload that fails or is
 * paused resumes from the last byte the agent wrote, also after the page is
 * reloaded: tus-js-client remembers each file's upload URL in localStorage.
 *
 * The queue lives outside React, so it keeps going while the page shows
 * another folder.
 */

export type UploadState = "queued" | "uploading" | "paused" | "done" | "failed";

export interface UploadItem {
  id: number;
  name: string;
  location: string;
  /** The folder it goes into, relative to the location. */
  dir: string;
  size: number;
  sent: number;
  state: UploadState;
  /** Why it failed. */
  error?: string;
  /** Where it was saved (another name when its own was taken). */
  savedAs?: string;
}

/** How the queue reaches the agent. */
export interface UploadTarget {
  /** Resolves an upload path from the API (FileLocation.upload_path). */
  url: (path: string) => string;
  /** The Authorization header to send, or undefined when the browser's session cookie does. */
  authorization: () => Promise<string | undefined>;
  /** The agent refused the credentials; returns whether retrying may help. */
  onUnauthenticated: () => boolean;
}

interface Entry {
  item: UploadItem;
  file: File;
  endpoint: string;
  target: UploadTarget;
  upload?: Upload;
}

/** Retries after a dropped connection, then waits for the owner to resume. */
const retryDelays = [0, 1000, 3000, 5000, 10000, 20000, 30000];
const savedAsHeader = "Homecloud-Saved-As";

let entries: Entry[] = [];
let nextID = 1;
let snapshot: readonly UploadItem[] = [];
const listeners = new Set<() => void>();
const savedListeners = new Set<(location: string, dir: string) => void>();

function emit(): void {
  snapshot = entries.map((e) => e.item);
  for (const l of listeners) {
    l();
  }
}

function update(e: Entry, change: Partial<UploadItem>): void {
  e.item = { ...e.item, ...change };
  emit();
}

/** Queues files for upload into dir of location, through the location's upload path. */
export function enqueueUploads(
  target: UploadTarget,
  files: readonly File[],
  location: string,
  dir: string,
  uploadPath: string,
): void {
  for (const file of files) {
    entries.push({
      item: { id: nextID++, name: file.name, location, dir, size: file.size, sent: 0, state: "queued" },
      file,
      endpoint: target.url(uploadPath),
      target,
    });
  }
  emit();
  pump();
}

/** Starts the next queued upload when none is running. */
function pump(): void {
  if (entries.some((e) => e.item.state === "uploading")) {
    return;
  }
  const next = entries.find((e) => e.item.state === "queued");
  if (next !== undefined) {
    void start(next);
  }
}

async function start(e: Entry): Promise<void> {
  const t = e.target;
  update(e, { state: "uploading", error: undefined });
  let unauthenticatedRetried = false;
  const upload = new Upload(e.file, {
    endpoint: e.endpoint,
    metadata: { filename: e.item.name, dir: e.item.dir },
    retryDelays,
    storeFingerprintForResuming: true,
    removeFingerprintOnSuccess: true,
    onBeforeRequest: async (req) => {
      const auth = await t.authorization();
      if (auth !== undefined) {
        req.setHeader("Authorization", auth);
      }
    },
    // tus-js-client checks the number of retries before asking.
    onShouldRetry: (err) => {
      const status = err.originalResponse?.getStatus() ?? 0;
      if (status === 401) {
        // This PC's agent restarted with a new token: read it once more.
        const retry = !unauthenticatedRetried && t.onUnauthenticated();
        unauthenticatedRetried = true;
        return retry;
      }
      // tus-js-client's default: everything but a refusal (4xx), except 409
      // (the offset moved) and 423 (locked), while online.
      return (status < 400 || status >= 500 || status === 409 || status === 423) && navigator.onLine;
    },
    onProgress: (sent, total) => {
      update(e, { sent, size: total });
    },
    onSuccess: ({ lastResponse }) => {
      const saved = lastResponse.getHeader(savedAsHeader);
      update(e, {
        state: "done",
        sent: e.item.size,
        savedAs: saved === undefined ? undefined : decodeURIComponent(saved),
      });
      for (const l of savedListeners) {
        l(e.item.location, e.item.dir);
      }
      pump();
    },
    onError: (err) => {
      update(e, { state: "failed", error: describe(err) });
      pump();
    },
  });
  e.upload = upload;
  // Resume where an earlier attempt at the same file stopped.
  const previous = await upload.findPreviousUploads();
  if (previous[0] !== undefined) {
    upload.resumeFromPreviousUpload(previous[0]);
  }
  upload.start();
}

/** The agent's reason for a refusal, or what went wrong on the way. */
function describe(err: Error): string {
  if (err instanceof DetailedError && err.originalResponse !== null) {
    const body = err.originalResponse.getBody().trim();
    const status = err.originalResponse.getStatus();
    if (status === 507) {
      return "Not enough free space for this file.";
    }
    return body === "" ? `The agent answered ${String(status)}.` : body;
  }
  return "The connection to homecloud was lost.";
}

/** Pauses a running upload; resume carries on from where it stopped. */
export function pauseUpload(id: number): void {
  const e = entries.find((x) => x.item.id === id);
  if (e?.item.state !== "uploading") {
    return;
  }
  void e.upload?.abort(false);
  update(e, { state: "paused" });
  pump();
}

/** Queues a paused or failed upload again. */
export function resumeUpload(id: number): void {
  const e = entries.find((x) => x.item.id === id);
  if (e === undefined || (e.item.state !== "paused" && e.item.state !== "failed")) {
    return;
  }
  update(e, { state: "queued", error: undefined });
  pump();
}

/** Cancels an upload: the agent removes what it received. */
export function cancelUpload(id: number): void {
  const e = entries.find((x) => x.item.id === id);
  if (e === undefined || e.item.state === "done") {
    return;
  }
  const running = e.item.state === "uploading";
  entries = entries.filter((x) => x !== e);
  emit();
  void e.upload?.abort(true).catch(() => {
    // Nothing was created yet, or the agent removes it at upload_expiry.
  });
  if (running) {
    pump();
  }
}

/** Removes finished uploads from the list. */
export function clearFinishedUploads(): void {
  entries = entries.filter((e) => e.item.state !== "done");
  emit();
}

/** Whether any upload has not finished; leaving the page would stop it. */
export function uploadsPending(): boolean {
  return entries.some((e) => e.item.state === "queued" || e.item.state === "uploading");
}

/** Calls fn whenever a file has been saved into dir of location. */
export function onUploadSaved(fn: (location: string, dir: string) => void): () => void {
  savedListeners.add(fn);
  return () => {
    savedListeners.delete(fn);
  };
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

/** The uploads; components re-render when they change. */
export function useUploads(): readonly UploadItem[] {
  return useSyncExternalStore(subscribe, () => snapshot);
}
