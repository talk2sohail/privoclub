import { describe, expect, it } from "vitest";
import { hiddenCount, lineDiff, previousOf, reveal } from "./review";
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

describe("reveal (U-02, review of 2026-11-15)", () => {
  it("shows characters a browser does not draw, and keeps line feeds and tabs", () => {
    // "Reads a file." followed by Unicode tag characters spelling "IGNORE",
    // a zero-width space, a right-to-left override and a variation selector.
    const tags = [..."IGNORE"].map((c) => String.fromCodePoint(0xe0000 + c.charCodeAt(0))).join("");
    const s = "Reads a file." + tags + "\u200b" + "\u202e" + "x\ufe0f" + "\n\tok";
    expect(hiddenCount(s)).toBe(9);
    const r = reveal(s);
    expect(r).toContain("⟦U+E0049⟧⟦U+E0047⟧");
    expect(r).toContain("⟦U+200B⟧⟦U+202E⟧x⟦U+FE0F⟧\n\tok");
    expect(hiddenCount(r)).toBe(0);
  });
  it("leaves ordinary text, accents and emoji alone", () => {
    const s = "Crée un fichier — 東京 🙂";
    expect(reveal(s)).toBe(s);
    expect(hiddenCount(s)).toBe(0);
  });
});
