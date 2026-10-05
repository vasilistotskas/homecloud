import type { Transport } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";

/**
 * The agent serves this app, so the API is same-origin and the browser sends
 * the session cookie with every call.
 */
export function browserTransport(): Transport {
  return createConnectTransport({ baseUrl: window.location.origin });
}
