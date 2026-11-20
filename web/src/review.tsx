// What a reviewer reads before accepting a tool, prompt or resource (F11, the
// F14 review's R-01): the whole definition, what it replaces, and signs of
// hidden instructions. Upstream text is only ever rendered as text.

import type { Finding, Pin } from "./types";

/** reveal shows characters a person cannot see as ⟦U+XXXX⟧, as the server does. */
export function reveal(s: string): string {
  return s.replace(/[\p{Cf}\p{Co}\p{Cn}\p{Cs}\p{Cc}]/gu, (c) =>
    c === "\t" || c === "\n" || c === "\r" ? c : "⟦U+" + c.codePointAt(0)!.toString(16).toUpperCase().padStart(4, "0") + "⟧",
  );
}

/** stableJSON is JSON with keys sorted, two-space indented: equal values print the same. */
export function stableJSON(v: unknown): string {
  const sort = (x: unknown): unknown => {
    if (Array.isArray(x)) return x.map(sort);
    if (x && typeof x === "object") {
      return Object.fromEntries(
        Object.keys(x as Record<string, unknown>)
          .sort()
          .map((k) => [k, sort((x as Record<string, unknown>)[k])]),
      );
    }
    return x;
  };
  return JSON.stringify(sort(v), null, 2) ?? "";
}

export type DiffLine = { op: " " | "+" | "-"; text: string };

/** MAX_DIFF bounds the work of a diff (lines before × lines after). */
export const MAX_DIFF = 4_000_000;

/** lineDiff is the longest-common-subsequence diff of two texts, line by line,
 * or null when they are too long to compare here. */
export function lineDiff(before: string, after: string): DiffLine[] | null {
  const a = before.split("\n");
  const b = after.split("\n");
  if (a.length * b.length > MAX_DIFF) return null;
  const n = a.length;
  const m = b.length;
  const lcs: Uint32Array[] = Array.from({ length: n + 1 }, () => new Uint32Array(m + 1));
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      lcs[i]![j] = a[i] === b[j] ? lcs[i + 1]![j + 1]! + 1 : Math.max(lcs[i + 1]![j]!, lcs[i]![j + 1]!);
    }
  }
  const out: DiffLine[] = [];
  let i = 0;
  let j = 0;
  while (i < n && j < m) {
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
  while (i < n) out.push({ op: "-", text: a[i++]! });
  while (j < m) out.push({ op: "+", text: b[j++]! });
  return out;
}

type Param = { name: string; type: string; required: boolean; description: string };

/** params lists the top-level inputs of a tool's schema. */
export function params(def: Record<string, unknown> | null): Param[] {
  const schema = (def?.inputSchema ?? def?.arguments) as { properties?: Record<string, Record<string, unknown>>; required?: string[] } | undefined;
  if (Array.isArray(schema)) {
    // A prompt's arguments: [{name, description, required}]
    return (schema as Record<string, unknown>[]).map((a) => ({
      name: String(a.name ?? ""),
      type: "text",
      required: a.required === true,
      description: String(a.description ?? ""),
    }));
  }
  const props = schema?.properties ?? {};
  return Object.keys(props)
    .sort()
    .map((name) => ({
      name,
      type: String(props[name]!.type ?? (props[name]!.enum ? "one of" : "")),
      required: (schema?.required ?? []).includes(name),
      description: String(props[name]!.description ?? ""),
    }));
}

export function warnings(p: Pin): number {
  return (p.findings ?? []).filter((f) => f.severity === "warn").length;
}

function Findings({ list }: { list: Finding[] }) {
  if (list.length === 0) return <p class="meta">No signs of hidden instructions found. That proves nothing: read it anyway.</p>;
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

function Diff({ before, after }: { before: unknown; after: unknown }) {
  const lines = lineDiff(stableJSON(before), stableJSON(after));
  if (!lines) return <p class="meta">Too long to compare here; read the whole definition below.</p>;
  return (
    <pre class="args diff" aria-label="What changed">
      {lines.map((l, i) => (
        <span key={i} class={l.op === "+" ? "add" : l.op === "-" ? "del" : undefined}>
          {l.op} {reveal(l.text)}
          {"\n"}
        </span>
      ))}
    </pre>
  );
}

/** PinDetail is everything the model would read in a definition. */
export function PinDetail({ pin }: { pin: Pin }) {
  const def = pin.definition;
  const description = typeof def?.description === "string" ? def.description : "";
  const ps = params(def);
  return (
    <div class="pin-detail">
      <Findings list={pin.findings ?? []} />
      {pin.previous && (
        <>
          <h3>What changed since the accepted version</h3>
          <Diff before={pin.previous.definition} after={def} />
        </>
      )}
      <h3>Description</h3>
      {description ? <pre class="args">{reveal(description)}</pre> : <p class="meta">No description.</p>}
      {ps.length > 0 && (
        <>
          <h3>Inputs</h3>
          <div class="list-wrap">
            <table class="list">
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Type</th>
                  <th>Description</th>
                </tr>
              </thead>
              <tbody>
                {ps.map((p) => (
                  <tr key={p.name}>
                    <td>
                      <code>{reveal(p.name)}</code>
                      {p.required && <small class="meta"> required</small>}
                    </td>
                    <td>{p.type}</td>
                    <td class="pre-wrap">{reveal(p.description)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}
      <details>
        <summary>The whole definition</summary>
        <pre class="args">{reveal(stableJSON(def))}</pre>
      </details>
    </div>
  );
}
