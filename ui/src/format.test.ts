import { describe, expect, it } from "vitest";

import { formatBytes, formatDuration, formatInterval, formatWhen } from "./format";

describe("formatBytes", () => {
  it("uses binary units", () => {
    expect(formatBytes(0)).toBe("0 B");
    expect(formatBytes(1536)).toBe("1.5 KiB");
    expect(formatBytes(285_942_383_675n)).toBe("266 GiB");
  });
});

describe("formatDuration", () => {
  it("scales units", () => {
    expect(formatDuration(38_000)).toBe("38 s");
    expect(formatDuration(72_000)).toBe("1 min 12 s");
    expect(formatDuration(120_000)).toBe("2 min");
    expect(formatDuration(7_500_000)).toBe("2 h 5 min");
  });
});

describe("formatInterval", () => {
  it("prefers hours", () => {
    expect(formatInterval(3600n)).toBe("every hour");
    expect(formatInterval(7200n)).toBe("every 2 hours");
    expect(formatInterval(1800n)).toBe("every 30 minutes");
  });
});

describe("formatWhen", () => {
  it("names today and yesterday", () => {
    const now = new Date(2026, 9, 5, 18, 0);
    expect(formatWhen(new Date(2026, 9, 5, 14, 0), now)).toMatch(/^Today /);
    expect(formatWhen(new Date(2026, 9, 4, 9, 12), now)).toMatch(/^Yesterday /);
    expect(formatWhen(new Date(2026, 9, 1, 9, 12), now)).not.toMatch(/^(Today|Yesterday)/);
  });
});
