// The shell: the bar with the pages this role can use, and the page itself.

import type { ComponentChildren } from "preact";
import { useEffect } from "preact/hooks";
import type { Can, Session } from "./api";
import { Activity } from "./pages/activity";
import { Approvals } from "./pages/approvals";
import { Connections } from "./pages/connections";
import { People } from "./pages/people";
import { Rules } from "./pages/rules";
import { Server } from "./pages/server";
import { Servers } from "./pages/servers";
import { Start } from "./pages/start";
import { Teams } from "./pages/teams";
import { OAuthClients } from "./pages/oauth";
import { Tokens } from "./pages/tokens";
import { Link, beforeNavigate, match, navigate, useLocation } from "./router";
import { FlashBar, clearFlash } from "./ui";

type NavItem = { href: string; label: string; show: (c: Can) => boolean };

export const navItems: NavItem[] = [
  { href: "/admin/start", label: "Start", show: (c) => c.use_gateway },
  { href: "/admin/approvals", label: "Approvals", show: (c) => c.decide },
  { href: "/admin/servers", label: "Servers", show: (c) => c.servers },
  { href: "/admin/teams", label: "Teams", show: (c) => c.teams },
  { href: "/admin/rules", label: "Rules", show: (c) => c.rules },
  { href: "/admin/people", label: "People", show: (c) => c.people },
  { href: "/admin/tokens", label: "Tokens", show: (c) => c.use_gateway || c.manage_tokens },
  { href: "/admin/clients", label: "Clients", show: (c) => c.manage_tokens },
  { href: "/admin/activity", label: "Activity", show: (c) => c.activity },
  { href: "/admin/connections", label: "Connections", show: (c) => c.connections },
];

export type PageProps = { session: Session; loc: string };

type Route = { pattern: string; allowed: (c: Can) => boolean; render: (p: PageProps, params: Record<string, string>) => ComponentChildren };

const routes: Route[] = [
  { pattern: "/admin/start", allowed: (c) => c.use_gateway, render: (p) => <Start {...p} /> },
  { pattern: "/admin/teams", allowed: (c) => c.teams, render: (p) => <Teams {...p} /> },
  { pattern: "/admin/servers", allowed: (c) => c.servers, render: (p) => <Servers {...p} /> },
  { pattern: "/admin/servers/:alias", allowed: (c) => c.servers, render: (p, m) => <Server {...p} alias={m.alias ?? ""} /> },
  { pattern: "/admin/rules", allowed: (c) => c.rules, render: (p) => <Rules {...p} /> },
  { pattern: "/admin/people", allowed: (c) => c.people, render: (p) => <People {...p} /> },
  { pattern: "/admin/tokens", allowed: (c) => c.use_gateway || c.manage_tokens, render: (p) => <Tokens {...p} /> },
  { pattern: "/admin/clients", allowed: (c) => c.manage_tokens, render: (p) => <OAuthClients {...p} /> },
  { pattern: "/admin/activity", allowed: (c) => c.activity, render: (p) => <Activity {...p} /> },
  { pattern: "/admin/approvals", allowed: (c) => c.decide, render: (p) => <Approvals {...p} /> },
  { pattern: "/admin/connections", allowed: (c) => c.connections, render: (p) => <Connections {...p} /> },
];

function currentPage(session: Session, loc: string): ComponentChildren {
  for (const r of routes) {
    const m = match(r.pattern, loc);
    if (!m) continue;
    if (!r.allowed(session.can)) {
      return (
        <>
          <h1>Not allowed</h1>
          <p>Your role cannot use this page.</p>
        </>
      );
    }
    return r.render({ session, loc }, m);
  }
  return (
    <>
      <h1>Not found</h1>
      <p>
        There is no such page. <Link href={session.landing}>Go to the start</Link>.
      </p>
    </>
  );
}

export function App({ session }: { session: Session }) {
  const loc = useLocation();
  const path = loc.split("?")[0] ?? "";

  useEffect(() => {
    beforeNavigate.add(clearFlash);
    return () => {
      beforeNavigate.delete(clearFlash);
    };
  }, []);

  useEffect(() => {
    if (path === "/admin" || path === "/admin/") navigate(session.landing, { replace: true });
  }, [path, session.landing]);

  useEffect(() => {
    const item = navItems.find((n) => path === n.href || path.startsWith(n.href + "/"));
    document.title = (item ? item.label + " · " : "") + "Privo";
  }, [path]);

  const u = session.user;
  return (
    <>
      <header class="bar">
        <Link class="brand" href={session.landing}>
          Privo
        </Link>
        <nav aria-label="Pages">
          {navItems
            .filter((n) => n.show(session.can))
            .map((n) => (
              <Link
                key={n.href}
                href={n.href}
                class={path === n.href || path.startsWith(n.href + "/") ? "current" : undefined}
                aria-current={path === n.href ? "page" : undefined}
              >
                {n.label}
              </Link>
            ))}
        </nav>
        <form class="who" method="post" action="/admin/signout">
          <span>
            {u.name}
            {u.name !== u.person && <small> {u.person}</small>} <small>{u.role}</small>
          </span>
          <input type="hidden" name="csrf" value={session.csrf} />
          <button type="submit" class="link">
            Sign out
          </button>
        </form>
      </header>
      <main>
        <FlashBar />
        {currentPage(session, loc)}
      </main>
    </>
  );
}
