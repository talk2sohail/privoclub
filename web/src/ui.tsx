// Small shared pieces. Everything shown is rendered as text by Preact; the app
// never sets HTML from data.

import type { ComponentChildren } from "preact";
import { useEffect, useState } from "preact/hooks";

// ---- the flash: one message under the page title

export type Flash = { kind: "ok" | "warn"; text: string } | null;

let current: Flash = null;
const flashListeners = new Set<(f: Flash) => void>();

export function flash(kind: "ok" | "warn", text: string): void {
  current = { kind, text };
  for (const f of flashListeners) f(current);
}

export function clearFlash(): void {
  current = null;
  for (const f of flashListeners) f(null);
}

export function FlashBar() {
  const [f, setF] = useState<Flash>(current);
  useEffect(() => {
    flashListeners.add(setF);
    return () => {
      flashListeners.delete(setF);
    };
  }, []);
  if (!f) return null;
  return (
    <p class={"flash " + f.kind} role="status">
      {f.text}
      <button type="button" class="link close" aria-label="Dismiss" onClick={clearFlash}>
        ×
      </button>
    </p>
  );
}

// ---- running an action: a busy button, and the API's message on failure

/**
 * useAction runs a change: busy while it runs, the outcome in the flash, and
 * the last error also returned, so a form can show it beside itself (a form
 * low on a page is out of view of the flash).
 */
export function useAction(): [boolean, (fn: () => Promise<unknown>, ok?: string) => Promise<boolean>, string] {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const run = async (fn: () => Promise<unknown>, ok?: string): Promise<boolean> => {
    setBusy(true);
    setError("");
    try {
      await fn();
      if (ok) flash("ok", ok);
      return true;
    } catch (e) {
      const msg = (e as Error).message;
      setError(msg);
      flash("warn", msg);
      return false;
    } finally {
      setBusy(false);
    }
  };
  return [busy, run, error];
}

/** formValues reads a submitted form as strings. */
export function formValues(e: Event): Record<string, string> {
  e.preventDefault();
  const out: Record<string, string> = {};
  new FormData(e.currentTarget as HTMLFormElement).forEach((v, k) => {
    if (typeof v === "string") out[k] = v;
  });
  return out;
}

// ---- display helpers

export function when(iso: string | null | undefined): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime()) || d.getFullYear() < 2000) return "";
  return d.toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
}

/** firstLine shortens upstream text for a table cell. */
export function firstLine(s: string | undefined, limit = 300): string {
  const line = (s ?? "").trim().split("\n")[0] ?? "";
  return line.length > limit ? line.slice(0, limit) + "…" : line;
}

export function Status({ value }: { value: string }) {
  return <span class={"status " + value}>{value}</span>;
}

export function Empty({ children }: { children: ComponentChildren }) {
  return <p class="empty">{children}</p>;
}

export function Loading({ what }: { what: string }) {
  return <p class="empty loading">Loading {what}…</p>;
}

export function ErrorLine({ text }: { text: string }) {
  return text ? <p class="flash warn">{text}</p> : null;
}

/** Copy is a block of text the person copies (a token, a link, a command). */
export function Copy({ text, label }: { text: string; label: string }) {
  const [done, setDone] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text);
      setDone(true);
      setTimeout(() => setDone(false), 2000);
    } catch {
      setDone(false);
    }
  };
  return (
    <div class="copy">
      <pre class="args">{text}</pre>
      <button type="button" onClick={copy} aria-label={"Copy " + label}>
        {done ? "Copied" : "Copy"}
      </button>
    </div>
  );
}
