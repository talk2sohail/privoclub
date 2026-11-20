import type { Finding, Pin } from "./types";

// F14 R-01: what is accepted is the whole definition (its hash covers the
// description, the input schema and the annotations), so the review shows the
// whole of it, as text, and for a changed tool what changed.

/** pretty is a definition as indented JSON, the form the review shows. */
export function pretty(def: unknown): string {
  try {
    return JSON.stringify(def ?? null, null, 2) ?? "null";
  } catch {
    return String(def);
  }
}

// Characters a browser draws as nothing, or that reorder what is drawn, but
// that a model reads: control and format characters (zero-width, bidi
// overrides, Unicode tag characters used to smuggle text), private use,
// unassigned code points, line and paragraph separators, and variation
// selectors. The accepted hash covers them, so the review must show them.
// Line feed and tab are kept: they are visible as layout.
const hidden = /(?![\n\t])[\p{Cc}\p{Cf}\p{Co}\p{Cn}\p{Zl}\p{Zp}\u{FE00}-\u{FE0F}\u{E0100}-\u{E01EF}]/gu;

/** reveal shows every hidden character as a visible ⟦U+XXXX⟧ escape. */
export function reveal(s: string): string {
  return s.replace(hidden, (c) => `⟦U+${c.codePointAt(0)!.toString(16).toUpperCase().padStart(4, "0")}⟧`);
}

/** hiddenCount is how many hidden characters s holds. */
export function hiddenCount(s: string): number {
  return s.match(hidden)?.length ?? 0;
}

export type DiffLine = { op: " " | "+" | "-"; text: string };

// Above this many lines in both versions the diff is not computed (it is
// quadratic); both versions are shown instead.
const maxDiffLines = 1500;

/** lineDiff is a line diff of two texts (longest common subsequence). */
export function lineDiff(before: string, after: string): DiffLine[] | null {
  const a = before.split("\n");
  const b = after.split("\n");
  if (a.length * b.length > maxDiffLines * maxDiffLines) return null;
  // lcs[i][j] is the length of the longest common subsequence of a[i:] and b[j:].
  const lcs: number[][] = Array.from({ length: a.length + 1 }, () => new Array<number>(b.length + 1).fill(0));
  for (let i = a.length - 1; i >= 0; i--) {
    for (let j = b.length - 1; j >= 0; j--) {
      lcs[i]![j] = a[i] === b[j] ? lcs[i + 1]![j + 1]! + 1 : Math.max(lcs[i + 1]![j]!, lcs[i]![j + 1]!);
    }
  }
  const out: DiffLine[] = [];
  let i = 0;
  let j = 0;
  while (i < a.length && j < b.length) {
    if (a[i] === b[j]) {
      out.push({ op: " ", text: a[i]! });
      i++;
      j++;
    } else if (lcs[i + 1]![j]! >= lcs[i]![j + 1]!) {
      out.push({ op: "-", text: a[i++]! });
    } else {
      out.push({ op: "+", text: b[j++]! });
    }
  }
  while (i < a.length) out.push({ op: "-", text: a[i++]! });
  while (j < b.length) out.push({ op: "+", text: b[j++]! });
  return out;
}

/** Definition shows a tool's whole description and, on request, its whole definition. */
export function Definition({ pin, previous }: { pin: Pin; previous?: Pin }) {
  // Everything shown goes through reveal: what is accepted includes characters
  // a browser would not draw (U-02, review of 2026-11-15).
  const desc = reveal(pin.definition?.description ?? "");
  const raw = pretty(pin.definition);
  const now = reveal(raw);
  const was = previous ? reveal(pretty(previous.definition)) : "";
  const diff = previous ? lineDiff(was, now) : null;
  const n = hiddenCount(raw);
  return (
    <div class="definition">
      {n > 0 && (
        <p class="hidden-warning" role="alert">
          This definition holds {n} hidden {n === 1 ? "character" : "characters"} that a browser does not draw, shown as ⟦U+…⟧. A model reads
          them. Accept only if you know why they are there.
        </p>
      )}
      {previous && <p class="changed">Changed since it was accepted. What changed is shown below.</p>}
      {desc ? <p class="description">{desc}</p> : <p class="meta">No description.</p>}
      {previous && diff && (
        <pre class="diff" aria-label={"What changed in " + pin.name}>
          {diff.map((l, k) => (
            <span key={k} class={l.op === "+" ? "add" : l.op === "-" ? "del" : undefined}>
              {l.op} {l.text}
              {"\n"}
            </span>
          ))}
        </pre>
      )}
      {previous && !diff && (
        <>
          <p class="meta">Too large to compare line by line. The accepted version:</p>
          <pre>{was}</pre>
        </>
      )}
      <details>
        <summary>Full definition (what is accepted)</summary>
        <pre>{now}</pre>
      </details>
    </div>
  );
}

/** previousOf finds the accepted version of a pending pin, if it changed. */
export function previousOf(pin: Pin, all: Pin[]): Pin | undefined {
  return all.find((p) => p.status === "accepted" && p.kind === pin.kind && p.name === pin.name && p.hash !== pin.hash);
}

/** warnings counts the scan's warnings on a definition. */
export function warnings(p: Pin): number {
  return (p.findings ?? []).filter((f) => f.severity === "warn").length;
}

/** Findings lists what the scan found (F11): a hint for the reviewer, never a decision. */
export function Findings({ list }: { list: Finding[] }) {
  if (list.length === 0) return null;
  return (
    <ul class="findings">
      {list.map((f, i) => (
        <li key={i} class={f.severity}>
          <strong>{f.severity === "warn" ? "Warning" : "Note"}:</strong> {f.message} <small class="meta">in {f.path || "the definition"}</small>
          {f.excerpt && <code class="excerpt">{f.excerpt}</code>}
        </li>
      ))}
    </ul>
  );
}
