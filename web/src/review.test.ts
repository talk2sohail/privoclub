import { describe, expect, it } from "vitest";
import { MAX_DIFF, lineDiff, params, reveal, stableJSON } from "./review";

describe("review helpers", () => {
  it("reveals characters a person cannot see, keeping tabs and newlines", () => {
    expect(reveal("a​b‮c\td\ne\u{E0041}")).toBe("a⟦U+200B⟧b⟦U+202E⟧c\td\ne⟦U+E0041⟧");
    expect(reveal("plain <b>text</b>")).toBe("plain <b>text</b>");
  });
  it("prints equal values the same whatever their key order", () => {
    expect(stableJSON({ b: 1, a: { d: 2, c: [3, { f: 4, e: 5 }] } })).toBe(stableJSON({ a: { c: [3, { e: 5, f: 4 }], d: 2 }, b: 1 }));
  });
  it("diffs lines", () => {
    expect(lineDiff("a\nb\nc", "a\nx\nc")).toEqual([
      { op: " ", text: "a" },
      { op: "-", text: "b" },
      { op: "+", text: "x" },
      { op: " ", text: "c" },
    ]);
    expect(lineDiff("same", "same")).toEqual([{ op: " ", text: "same" }]);
    const long = Array.from({ length: Math.ceil(Math.sqrt(MAX_DIFF)) + 1 }, (_, i) => String(i)).join("\n");
    expect(lineDiff(long, long + "\nmore")).toBeNull();
  });
  it("lists a tool's inputs and a prompt's arguments", () => {
    expect(
      params({ inputSchema: { type: "object", properties: { repo: { type: "string", description: "Repo" }, n: { type: "integer" } }, required: ["repo"] } }),
    ).toEqual([
      { name: "n", type: "integer", required: false, description: "" },
      { name: "repo", type: "string", required: true, description: "Repo" },
    ]);
    expect(params({ arguments: [{ name: "topic", description: "What", required: true }] })).toEqual([
      { name: "topic", type: "text", required: true, description: "What" },
    ]);
    expect(params(null)).toEqual([]);
  });
});
