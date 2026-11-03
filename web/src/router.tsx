// A small history router: the app's pages are real URLs (/admin/servers),
// and moving between them never loads a page.

import type { ComponentChildren, JSX } from "preact";
import { useEffect, useState } from "preact/hooks";

const listeners = new Set<() => void>();

/** beforeNavigate runs before every move to another page (the shell clears the flash). */
export const beforeNavigate = new Set<() => void>();

function notify(): void {
  for (const f of listeners) f();
}

if (typeof window !== "undefined") {
  window.addEventListener("popstate", () => {
    for (const f of beforeNavigate) f();
    notify();
  });
}

export function here(): string {
  return window.location.pathname + window.location.search;
}

export function navigate(to: string, opts: { replace?: boolean } = {}): void {
  if (to === here()) return;
  for (const f of beforeNavigate) f();
  if (opts.replace) window.history.replaceState(null, "", to);
  else window.history.pushState(null, "", to);
  window.scrollTo(0, 0);
  notify();
}

/** useLocation is the current path and query, updated on every navigation. */
export function useLocation(): string {
  const [loc, setLoc] = useState(here);
  useEffect(() => {
    const f = () => setLoc(here());
    listeners.add(f);
    return () => {
      listeners.delete(f);
    };
  }, []);
  return loc;
}

/** match matches "/admin/servers/:alias" against a path; null if it does not. */
export function match(pattern: string, path: string): Record<string, string> | null {
  const want = pattern.split("/");
  const got = (path.split("?")[0] ?? "").split("/");
  if (want.length !== got.length) return null;
  const params: Record<string, string> = {};
  for (let i = 0; i < want.length; i++) {
    const w = want[i] ?? "";
    const g = got[i] ?? "";
    if (w.startsWith(":")) {
      if (!g) return null;
      try {
        params[w.slice(1)] = decodeURIComponent(g);
      } catch {
        return null;
      }
    } else if (w !== g) {
      return null;
    }
  }
  return params;
}

/** query reads one parameter of the current URL. */
export function query(loc: string, name: string): string {
  const i = loc.indexOf("?");
  return i < 0 ? "" : (new URLSearchParams(loc.slice(i + 1)).get(name) ?? "");
}

type LinkProps = Omit<JSX.HTMLAttributes<HTMLAnchorElement>, "href"> & { href: string; children?: ComponentChildren };

/** Link is an <a> that navigates inside the app on a plain click. */
export function Link({ href, children, ...rest }: LinkProps) {
  const onClick = (e: MouseEvent) => {
    if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    e.preventDefault();
    navigate(href);
  };
  return (
    <a href={href} onClick={onClick} {...rest}>
      {children}
    </a>
  );
}
