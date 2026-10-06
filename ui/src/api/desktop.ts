import type { DescMessage, DescMethodStreaming, DescMethodUnary, MessageInitShape } from "@bufbuild/protobuf";
import {
  Code,
  ConnectError,
  createClient,
  type ContextValues,
  type Interceptor,
  type StreamResponse,
  type Transport,
  type UnaryResponse,
} from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { invoke } from "@tauri-apps/api/core";

import type { DesktopConnection, Platform } from "../app/platformContext";
import { AgentService } from "../gen/homecloud/agent/v3/agent_pb";

/** Which agent the desktop shell uses (desktop/src-tauri ConnectionSettings). */
type ConnectionSettings = { kind: "this_pc" } | { kind: "remote"; url: string; device_id: string };

/** The address and token the desktop shell calls the agent with. */
interface Credentials {
  url: string;
  token: string;
}

/** Reads which agent the desktop app uses. */
export async function desktopConnection(): Promise<DesktopConnection> {
  const s = await invoke<ConnectionSettings>("connection_settings");
  return s.kind === "this_pc" ? { kind: "this_pc" } : { kind: "remote", url: s.url, deviceId: s.device_id };
}

function bearer(token: string): Interceptor {
  return (next) => (req) => {
    req.header.set("Authorization", `Bearer ${token}`);
    return next(req);
  };
}

/**
 * Talks to the agent the desktop app uses with its credentials. This PC's
 * agent issues a new token every time it starts, so they are re-read
 * whenever it rejects the current one; a paired device's token is rejected
 * only once it is revoked, which no retry can mend.
 */
export class DesktopTransport implements Transport {
  private inner: Promise<Transport> | undefined;
  /**
   * Where API and media requests go once the credentials have been read: the
   * agent itself, or, under `tauri dev` for this PC, the Vite dev server that
   * proxies to it (the agent accepts only its own origin and the packaged
   * app's).
   */
  baseUrl = "";

  constructor(readonly connection: DesktopConnection) {}

  private current(): Promise<Transport> {
    this.inner ??= invoke<Credentials>("connection_credentials").then(
      (c) => {
        this.baseUrl = import.meta.env.DEV && this.connection.kind === "this_pc" ? window.location.origin : c.url;
        return createConnectTransport({ baseUrl: this.baseUrl, interceptors: [bearer(c.token)] });
      },
      (err: unknown) => {
        this.inner = undefined;
        throw new ConnectError(String(err), Code.Unavailable);
      },
    );
    return this.inner;
  }

  /** Forgets the address and token; the next call reads them again. */
  invalidate(): void {
    this.inner = undefined;
  }

  async unary<I extends DescMessage, O extends DescMessage>(
    method: DescMethodUnary<I, O>,
    signal: AbortSignal | undefined,
    timeoutMs: number | undefined,
    header: HeadersInit | undefined,
    input: MessageInitShape<I>,
    contextValues?: ContextValues,
  ): Promise<UnaryResponse<I, O>> {
    const call = async () => (await this.current()).unary(method, signal, timeoutMs, header, input, contextValues);
    try {
      return await call();
    } catch (err) {
      if (this.connection.kind !== "this_pc" || ConnectError.from(err).code !== Code.Unauthenticated) {
        throw err;
      }
      this.invalidate();
      return call();
    }
  }

  async stream<I extends DescMessage, O extends DescMessage>(
    method: DescMethodStreaming<I, O>,
    signal: AbortSignal | undefined,
    timeoutMs: number | undefined,
    header: HeadersInit | undefined,
    input: AsyncIterable<MessageInitShape<I>>,
    contextValues?: ContextValues,
  ): Promise<StreamResponse<I, O>> {
    return (await this.current()).stream(method, signal, timeoutMs, header, input, contextValues);
  }
}

/**
 * Pairs this computer with the homecloud at url using a one-time code from
 * `homecloud open --print` there, and uses it from now on.
 */
async function pair(url: string, code: string, name: string): Promise<void> {
  const client = createClient(AgentService, createConnectTransport({ baseUrl: url }));
  const res = await client.pairDevice({ code, name });
  await invoke("connect_remote", { url, deviceId: res.deviceId, token: res.token });
}

/** The desktop shell's connection and operating-system actions (desktop/src-tauri/src/lib.rs). */
export function desktopPlatform(transport: DesktopTransport): Platform {
  return {
    kind: "desktop",
    connection: transport.connection,
    // Media URLs are paths on the agent, which is not the webview's origin.
    mediaUrl: (path) => transport.baseUrl + path,
    startAgent: () => invoke("start_agent"),
    revealInFolder: (path) => invoke("reveal_in_folder", { path }),
    onUnauthenticated: () => {
      transport.invalidate();
    },
    deviceName: () => invoke<string>("device_name"),
    pair: async (url, code, name) => {
      await pair(url, code, name);
      window.location.reload();
    },
    disconnect: async () => {
      if (transport.connection.kind === "remote") {
        // Revoke this device on the other side first; a device it can no
        // longer reach is revoked from there (Settings, Paired devices).
        await createClient(AgentService, transport).revokeDevice({ deviceId: transport.connection.deviceId });
      }
      await invoke("use_this_pc");
      window.location.reload();
    },
    forget: async () => {
      await invoke("use_this_pc");
      window.location.reload();
    },
  };
}
