import type { Pin } from "./types";

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
  const desc = pin.definition?.description ?? "";
  const now = pretty(pin.definition);
  const diff = previous ? lineDiff(pretty(previous.definition), now) : null;
  return (
    <div class="definition">
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
          <pre>{pretty(previous.definition)}</pre>
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
