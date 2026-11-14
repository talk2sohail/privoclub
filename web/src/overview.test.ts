import { describe, expect, it } from "vitest";
import { bytes, ms, pct } from "./pages/overview";

describe("reading figures", () => {
  it("durations", () => {
    expect(ms(0)).toBe("–");
    expect(ms(0.42)).toBe("0.4 ms");
    expect(ms(12.4)).toBe("12 ms");
    expect(ms(1234)).toBe("1.23 s");
    expect(ms(45_000)).toBe("45.0 s");
  });
  it("sizes and rates", () => {
    expect(bytes(512)).toBe("512 B");
    expect(bytes(3500)).toBe("3.4 KB");
    expect(bytes(5 * 1024 * 1024)).toBe("5.0 MB");
    expect(pct(0)).toBe("0%");
    expect(pct(0.0004)).toBe("<0.1%");
    expect(pct(0.05)).toBe("5.0%");
    expect(pct(0.5)).toBe("50%");
  });
});
