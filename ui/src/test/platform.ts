import { expect } from "vitest";

import { browserPlatform } from "../api/transport";
import type { Platform } from "../app/platformContext";

/**
 * The browser platform for tests: an error the page logs fails the test.
 * mediaBase, when given, stands for an agent at another address.
 */
export function testPlatform(mediaBase = ""): Platform {
  const p = browserPlatform((m) => expect.unreachable(m));
  return mediaBase === "" ? p : { ...p, mediaUrl: (path) => mediaBase + path };
}
