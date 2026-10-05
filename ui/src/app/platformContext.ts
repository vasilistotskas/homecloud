import { createContext, use } from "react";

/** Where the app runs, and what that environment lets it do. */
export type Platform =
  | {
      /** Served by the agent to a browser; authenticated with a session cookie. */
      kind: "browser";
      /** Resolves a media path from the API; the agent serves this app, so it is same-origin. */
      mediaUrl: (path: string) => string;
    }
  | {
      /** Inside the desktop shell; authenticated with the agent's token. */
      kind: "desktop";
      /** Resolves a media path from the API against the agent's address. */
      mediaUrl: (path: string) => string;
      startAgent: () => Promise<void>;
      revealInFolder: (path: string) => Promise<void>;
      /** The agent rejected the token (it restarted); credentials are re-read. */
      onUnauthenticated: () => void;
    };

export const PlatformContext = createContext<Platform | undefined>(undefined);

export function usePlatform(): Platform {
  const ctx = use(PlatformContext);
  if (ctx === undefined) {
    throw new Error("usePlatform must be used inside PlatformContext");
  }
  return ctx;
}
