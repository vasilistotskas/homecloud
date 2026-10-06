import { render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { href, navigate, parseHash, useLocation } from "./router";

describe("parseHash", () => {
  it("reads the overview from no hash, # and #/", () => {
    for (const h of ["", "#", "#/"]) {
      expect(parseHash(h).path).toBe("/");
    }
  });

  it("reads a page and its search parameters", () => {
    const loc = parseHash("#/connect?code=ABCD-EFGH");
    expect(loc.path).toBe("/connect");
    expect(loc.search.get("code")).toBe("ABCD-EFGH");
    expect(parseHash("#/library").path).toBe("/library");
  });

  it("names no page for an unknown or inexact path", () => {
    for (const h of ["#/nope", "#/library/", "#/Library", "#library"]) {
      expect(parseHash(h).path).toBeUndefined();
    }
  });

  it("keeps a code made only of digits a string", () => {
    expect(parseHash("#/connect?code=23456789").search.get("code")).toBe("23456789");
  });
});

describe("href", () => {
  it("writes a path and its search parameters", () => {
    expect(href("/activity")).toBe("#/activity");
    expect(href("/connect", { code: "AB CD" })).toBe("#/connect?code=AB+CD");
  });
});

function Where() {
  const loc = useLocation();
  return <p>{`${loc.path ?? "none"} ${loc.search.toString()}`}</p>;
}

describe("navigate", () => {
  it("pushes a history entry and re-renders", async () => {
    render(<Where />);
    const before = window.history.length;
    navigate("/settings");
    expect(await screen.findByText("/settings")).toBeInTheDocument();
    expect(window.location.hash).toBe("#/settings");
    expect(window.history.length).toBe(before + 1);
  });

  it("replaces the current entry and still re-renders", async () => {
    render(<Where />);
    const before = window.history.length;
    navigate("/connect", { replace: true, search: { code: "ABCD-EFGH" } });
    expect(await screen.findByText("/connect code=ABCD-EFGH")).toBeInTheDocument();
    expect(window.history.length).toBe(before);
  });

  it("does nothing for the current location", async () => {
    navigate("/library");
    await waitFor(() => {
      expect(window.location.hash).toBe("#/library");
    });
    const before = window.history.length;
    navigate("/library");
    expect(window.history.length).toBe(before);
  });
});
