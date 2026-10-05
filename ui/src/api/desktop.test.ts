import { create } from "@bufbuild/protobuf";
import { Code, ConnectError, type Transport } from "@connectrpc/connect";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { AgentService, GetInfoResponseSchema } from "../gen/homecloud/agent/v1/agent_pb";
import { DesktopTransport } from "./desktop";

const invoke = vi.hoisted(() => vi.fn());
const createConnectTransport = vi.hoisted(() => vi.fn());
vi.mock("@tauri-apps/api/core", () => ({ invoke }));
vi.mock("@connectrpc/connect-web", () => ({ createConnectTransport }));

/** A fake transport that accepts only the given token. */
function agentAccepting(token: string, seen: string[]) {
  return (opts: { interceptors: ((next: unknown) => (req: { header: Headers }) => unknown)[] }) => {
    const headers = new Headers();
    opts.interceptors[0]?.(() => undefined)({ header: headers });
    const unary: Transport["unary"] = () => {
      const auth = headers.get("Authorization") ?? "";
      seen.push(auth);
      if (auth !== `Bearer ${token}`) {
        return Promise.reject(new ConnectError("missing or invalid credentials", Code.Unauthenticated));
      }
      return Promise.resolve({
        stream: false,
        service: AgentService,
        method: AgentService.method.getInfo,
        header: new Headers(),
        trailer: new Headers(),
        message: create(GetInfoResponseSchema, { apiVersion: "1.0" }),
      }) as never;
    };
    return { unary, stream: vi.fn() };
  };
}

const call = (t: DesktopTransport) => t.unary(AgentService.method.getInfo, undefined, undefined, undefined, {});

describe("DesktopTransport", () => {
  beforeEach(() => {
    invoke.mockReset();
    createConnectTransport.mockReset();
  });

  it("re-reads the endpoint once when the agent has a new token", async () => {
    const seen: string[] = [];
    createConnectTransport.mockImplementation(agentAccepting("new", seen));
    invoke
      .mockResolvedValueOnce({ url: "http://127.0.0.1:8790", token: "old", agent_version: "x" })
      .mockResolvedValueOnce({ url: "http://127.0.0.1:8790", token: "new", agent_version: "x" });

    const res = await call(new DesktopTransport());
    expect(res.message.apiVersion).toBe("1.0");
    expect(seen).toEqual(["Bearer old", "Bearer new"]);
    expect(invoke).toHaveBeenCalledTimes(2);
  });

  it("reports an agent that is not running as unavailable and tries again later", async () => {
    invoke.mockRejectedValueOnce("the agent is not running");
    const t = new DesktopTransport();
    await expect(call(t)).rejects.toMatchObject({ code: Code.Unavailable });

    const seen: string[] = [];
    createConnectTransport.mockImplementation(agentAccepting("tok", seen));
    invoke.mockResolvedValueOnce({ url: "http://127.0.0.1:8790", token: "tok", agent_version: "x" });
    await expect(call(t)).resolves.toBeDefined();
  });
});
