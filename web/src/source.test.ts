import { describe, expect, it } from "vitest";

// ADR 0019 point 6: data is only ever rendered as text.
const sources = import.meta.glob(["./**/*.{ts,tsx}", "!./**/*.test.{ts,tsx}", "!./test/**"], { query: "?raw", import: "default", eager: true }) as Record<
  string,
  string
>;

describe("the app's source", () => {
  it("never sets HTML from data or evaluates strings", () => {
    expect(Object.keys(sources).length).toBeGreaterThan(5);
    for (const [file, text] of Object.entries(sources)) {
      for (const bad of ["dangerouslySetInnerHTML", "innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval(", "new Function"]) {
        expect(text.includes(bad), `${file} uses ${bad}`).toBe(false);
      }
    }
  });
});
