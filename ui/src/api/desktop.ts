import type { DescMessage, DescMethodStreaming, DescMethodUnary, MessageInitShape } from "@bufbuild/protobuf";
import {
  Code,
  ConnectError,
  type ContextValues,
  type Interceptor,
  type StreamResponse,
  type Transport,
  type UnaryResponse,
} from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { invoke } from "@tauri-apps/api/core";

import type { Platform } from "../app/platformContext";

/** The agent's discovery record, as read by the desktop shell (internal/auth.Endpoint). */
interface Endpoint {
  url: string;
  token: string;
  agent_version: string;
}

function bearer(token: string): Interceptor {
  return (next) => (req) => {
    req.header.set("Authorization", `Bearer ${token}`);
    return next(req);
  };
}

/**
 * Talks to the agent named in its discovery file, with its bearer token. The
 * agent issues a new token every time it starts, so the endpoint is re-read
 * whenever the agent rejects the current one.
 */
export class DesktopTransport implements Transport {
  private inner: Promise<Transport> | undefined;
  /** The agent's address, known once its endpoint has been read. */
  baseUrl = "";

  private current(): Promise<Transport> {
    this.inner ??= invoke<Endpoint>("agent_endpoint").then(
      (ep) => {
        this.baseUrl = ep.url;
        return createConnectTransport({ baseUrl: ep.url, interceptors: [bearer(ep.token)] });
      },
      (err: unknown) => {
        this.inner = undefined;
        throw new ConnectError(String(err), Code.Unavailable);
      },
    );
    return this.inner;
  }

  /** Forgets the agent's address and token; the next call reads them again. */
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
      if (ConnectError.from(err).code !== Code.Unauthenticated) {
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

/** The desktop shell's operating-system actions (desktop/src-tauri/src/lib.rs). */
export function desktopPlatform(transport: DesktopTransport): Platform {
  return {
    kind: "desktop",
    // Media URLs are paths on the agent, which is not the webview's origin.
    mediaUrl: (path) => transport.baseUrl + path,
    startAgent: () => invoke("start_agent"),
    revealInFolder: (path) => invoke("reveal_in_folder", { path }),
    onUnauthenticated: () => {
      transport.invalidate();
    },
  };
}
