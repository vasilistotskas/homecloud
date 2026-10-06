import { createContext, use } from "react";

/** Which agent the desktop app uses. */
export type DesktopConnection =
  /** The agent on this computer, found through its discovery file. */
  | { kind: "this_pc" }
  /** A homecloud on another device (a NAS), paired once; its token is in the OS credential store. */
  | { kind: "remote"; url: string; deviceId: string };

/** Where the app runs, and what that environment lets it do. */
export type Platform =
  | {
      /** Served by the agent to a browser; authenticated with a session cookie. */
      kind: "browser";
      /** Resolves a media path from the API; the agent serves this app, so it is same-origin. */
      mediaUrl: (path: string) => string;
    }
  | {
      /** Inside the desktop shell; authenticated with the agent's or this device's token. */
      kind: "desktop";
      connection: DesktopConnection;
      /** Resolves a media path from the API against the agent's address. */
      mediaUrl: (path: string) => string;
      /** Starts this PC's agent (this_pc only). */
      startAgent: () => Promise<void>;
      /** Shows an archived file in this PC's file manager (this_pc only). */
      revealInFolder: (path: string) => Promise<void>;
      /** The agent rejected the token: this PC's agent restarted, and credentials are re-read. */
      onUnauthenticated: () => void;
      /** This computer's name, offered when pairing. */
      deviceName: () => Promise<string>;
      /** Pairs with the homecloud at url using a one-time code, then reloads. */
      pair: (url: string, code: string, name: string) => Promise<void>;
      /** Revokes this device on the other homecloud, uses this PC's agent again, then reloads. */
      disconnect: () => Promise<void>;
      /** Uses this PC's agent again without reaching the other homecloud, then reloads. */
      forget: () => Promise<void>;
    };

export const PlatformContext = createContext<Platform | undefined>(undefined);

export function usePlatform(): Platform {
  const ctx = use(PlatformContext);
  if (ctx === undefined) {
    throw new Error("usePlatform must be used inside PlatformContext");
  }
  return ctx;
}
