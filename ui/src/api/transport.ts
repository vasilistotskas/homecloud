import type { Transport } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";

import type { Platform } from "../app/platformContext";

/**
 * The agent serves this app, so the API is same-origin and the browser sends
 * the session cookie with every call.
 */
export function browserTransport(): Transport {
  return createConnectTransport({ baseUrl: window.location.origin });
}

/**
 * The app served by the agent to a browser: API, media and uploads are
 * same-origin and carry the session cookie.
 */
export function browserPlatform(logError: (message: string) => void): Platform {
  return {
    kind: "browser",
    mediaUrl: (path) => path,
    logError,
    upload: {
      url: (path) => path,
      authorization: () => Promise.resolve(undefined),
      // A session that ended is not mended by retrying.
      onUnauthenticated: () => false,
    },
  };
}
