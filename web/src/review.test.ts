import { describe, expect, it } from "vitest";
import { lineDiff, previousOf } from "./review";
import type { Pin } from "./types";

describe("lineDiff", () => {
  it("marks what was removed and added, in order", () => {
    expect(lineDiff("a\nb\nc", "a\nx\nc")).toEqual([
      { op: " ", text: "a" },
      { op: "-", text: "b" },
      { op: "+", text: "x" },
      { op: " ", text: "c" },
    ]);
    expect(lineDiff("a", "a")).toEqual([{ op: " ", text: "a" }]);
    expect(lineDiff("", "z")).toEqual([
      { op: "-", text: "" },
      { op: "+", text: "z" },
    ]);
  });
  it("gives up on very large texts instead of hanging the page", () => {
    const big = Array.from({ length: 2000 }, (_, i) => String(i)).join("\n");
    expect(lineDiff(big, big + "\nmore")).toBeNull();
  });
});

describe("previousOf", () => {
  const p = (name: string, hash: string, status: string, kind = "tool"): Pin => ({ kind, name, hash, status, definition: null });
  it("finds the accepted version of the same tool only", () => {
    const all = [p("read", "h1", "accepted"), p("read", "h2", "pending"), p("write", "h3", "accepted"), p("read", "h0", "blocked"), p("read", "h4", "accepted", "prompt")];
    expect(previousOf(all[1]!, all)?.hash).toBe("h1");
    expect(previousOf(p("new", "h9", "pending"), all)).toBeUndefined();
  });
});
