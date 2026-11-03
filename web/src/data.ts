// Loading data for a page, fast: a page shows the last answer for the same
// request at once (from memory) while it asks the server again.

import { useCallback, useEffect, useState } from "preact/hooks";
import { api } from "./api";

const cache = new Map<string, unknown>();
const stale = new Set<() => void>();

/** invalidate drops cached answers whose path starts with prefix and reloads the pages showing them. */
export function invalidate(prefix: string): void {
  for (const k of [...cache.keys()]) if (k.startsWith(prefix)) cache.delete(k);
  for (const f of stale) f();
}

/** clearCache forgets everything (tests, sign-out). */
export function clearCache(): void {
  cache.clear();
}

export type Loaded<T> = {
  data: T | undefined;
  error: string;
  loading: boolean;
  reload: () => void;
};

/**
 * useData GETs path (relative to /admin/api) and keeps the answer. With poll
 * (ms) it asks again on that interval while the page is visible. A null path
 * loads nothing.
 */
export function useData<T>(path: string | null, poll = 0): Loaded<T> {
  const [state, setState] = useState<{ data: T | undefined; error: string; loading: boolean }>(() => ({
    data: path ? (cache.get(path) as T | undefined) : undefined,
    error: "",
    loading: path !== null,
  }));
  const [tick, setTick] = useState(0);
  const reload = useCallback(() => setTick((t) => t + 1), []);

  useEffect(() => {
    stale.add(reload);
    return () => {
      stale.delete(reload);
    };
  }, [reload]);

  useEffect(() => {
    if (!path) return;
    let alive = true;
    setState((s) => ({ data: (cache.get(path) as T | undefined) ?? (tick > 0 ? s.data : undefined), error: "", loading: true }));
    const load = () =>
      api<T>("GET", path).then(
        (d) => {
          cache.set(path, d);
          if (alive) setState({ data: d, error: "", loading: false });
        },
        (e: Error) => {
          if (alive) setState((s) => ({ ...s, error: e.message, loading: false }));
        },
      );
    void load();
    let timer: ReturnType<typeof setInterval> | undefined;
    if (poll > 0) {
      timer = setInterval(() => {
        if (document.visibilityState === "visible") void load();
      }, poll);
    }
    return () => {
      alive = false;
      if (timer) clearInterval(timer);
    };
  }, [path, tick, poll]);

  return { ...state, reload };
}
