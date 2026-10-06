import { describe, expect, it } from "vitest";

import { remoteUrl } from "./remoteUrl";

describe("remoteUrl", () => {
  it("turns an address as typed into a lowercase https origin", () => {
    for (const typed of [
      "homecloud.tail1234.ts.net",
      " HomeCloud.Tail1234.ts.net ",
      "https://homecloud.tail1234.ts.net/",
      "http://homecloud.tail1234.ts.net",
    ]) {
      expect(remoteUrl(typed)).toBe("https://homecloud.tail1234.ts.net");
    }
  });
});
