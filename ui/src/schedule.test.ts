import { describe, expect, it } from "vitest";

import { nextRunAt } from "./schedule";

const hour = 3600n;
const at = (h: number) => new Date(Date.UTC(2026, 9, 6, h));

describe("nextRunAt", () => {
  it("is unknown without a next cycle", () => {
    expect(nextRunAt(at(0), hour, undefined, hour)).toBeUndefined();
  });

  it("runs at the next cycle when it never completed", () => {
    expect(nextRunAt(undefined, 24n * hour, at(5), hour)).toEqual(at(5));
  });

  it("runs at the next cycle when it is already due", () => {
    expect(nextRunAt(at(0), 2n * hour, at(5), hour)).toEqual(at(5));
  });

  it("runs at the first cycle at or after it is due", () => {
    // Due at 06:30; cycles at 05:00, 06:00, 07:00.
    expect(nextRunAt(new Date(Date.UTC(2026, 9, 6, 0, 30)), 6n * hour, at(5), hour)).toEqual(at(7));
    // Due exactly on a cycle.
    expect(nextRunAt(at(0), 6n * hour, at(5), hour)).toEqual(at(6));
  });
});
