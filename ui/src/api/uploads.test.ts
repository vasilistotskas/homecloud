import { describe, expect, it, vi } from "vitest";

import { enqueueUploads, type UploadTarget } from "./uploads";

interface Response {
  getStatus: () => number;
  getBody: () => string;
}

interface Options {
  metadata: Record<string, string>;
  onSuccess: (p: { lastResponse: { getHeader: (h: string) => string | undefined } }) => void;
  onError: (err: Error) => void;
}

// tus-js-client stands replaced by uploads the test drives.
const fake = vi.hoisted(() => {
  class FakeDetailedError extends Error {
    constructor(readonly originalResponse: Response | null) {
      super("tus");
    }
  }
  class FakeUpload {
    started = false;
    constructor(
      readonly file: File,
      readonly options: Options,
    ) {
      created.push(this);
    }
    findPreviousUploads() {
      return Promise.resolve([]);
    }
    resumeFromPreviousUpload() {
      // Nothing is stored in tests.
    }
    start() {
      this.started = true;
    }
    abort() {
      return Promise.resolve();
    }
    succeed(savedAs: string) {
      this.options.onSuccess({ lastResponse: { getHeader: () => encodeURIComponent(savedAs) } });
    }
  }
  const created: FakeUpload[] = [];
  return { FakeDetailedError, FakeUpload, created };
});

vi.mock("tus-js-client", () => ({ Upload: fake.FakeUpload, DetailedError: fake.FakeDetailedError }));

const target: UploadTarget = {
  url: (p) => p,
  authorization: () => Promise.resolve(undefined),
  onUnauthenticated: () => false,
};

async function flush() {
  await new Promise((r) => setTimeout(r, 0));
}

function setVisibility(state: DocumentVisibilityState) {
  Object.defineProperty(document, "visibilityState", { configurable: true, get: () => state });
  document.dispatchEvent(new Event("visibilitychange"));
}

describe("upload queue", () => {
  it("uploads one file at a time, and resumes one the connection cut off when the page is shown again", async () => {
    const { created } = fake;
    created.length = 0;
    enqueueUploads(target, [new File(["a"], "a.txt"), new File(["b"], "b.txt")], "Uploads", "Phone", "/u/");
    await flush();
    expect(created.map((u) => u.file.name)).toEqual(["a.txt"]);
    expect(created[0]?.options.metadata).toEqual({ filename: "a.txt", dir: "Phone" });

    // The phone locked: the request died without an answer.
    setVisibility("hidden");
    created[0]?.options.onError(new fake.FakeDetailedError(null));
    await flush();
    // The next file starts meanwhile.
    expect(created.map((u) => u.file.name)).toEqual(["a.txt", "b.txt"]);
    created[1]?.succeed("Phone/b.txt");
    await flush();

    setVisibility("visible");
    await flush();
    expect(created.map((u) => u.file.name)).toEqual(["a.txt", "b.txt", "a.txt"]);
    expect(created[2]?.started).toBe(true);
    created[2]?.succeed("Phone/a.txt");
  });

  it("does not retry an upload the agent refused", async () => {
    const { created } = fake;
    created.length = 0;
    enqueueUploads(target, [new File(["c"], "c.txt")], "Uploads", "", "/u/");
    await flush();
    created[0]?.options.onError(new fake.FakeDetailedError({ getStatus: () => 400, getBody: () => "bad name" }));
    setVisibility("visible");
    await flush();
    expect(created).toHaveLength(1);
  });
});
