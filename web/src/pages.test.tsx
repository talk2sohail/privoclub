import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/preact";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { loadSession } from "./api";
import { App } from "./app";
import { clearCache } from "./data";
import { navigate } from "./router";
import { fakeServer, session } from "./test/fake";
import { clearFlash } from "./ui";

const evil = `<img src=x onerror="window.pwned=1">`;

function at(path: string) {
  window.history.replaceState(null, "", path);
}

beforeEach(() => {
  clearCache();
  clearFlash();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

async function start(s: ReturnType<typeof session>, routes: Record<string, unknown>) {
  const srv = fakeServer({ "GET /admin/api/session": s, ...routes });
  await loadSession();
  render(<App session={s} />);
  return srv;
}

describe("the shell", () => {
  it("shows only the pages the role can use", async () => {
    at("/admin/tokens");
    await start(session("member"), { "GET /admin/api/v1/me/tokens": { tokens: [] } });
    const nav = screen.getByRole("navigation", { name: "Pages" });
    expect(nav.textContent).toContain("Tokens");
    expect(nav.textContent).toContain("Connections");
    for (const hidden of ["Approvals", "Servers", "Rules", "People", "Activity"]) expect(nav.textContent).not.toContain(hidden);
  });

  it("refuses a page the role cannot use, without asking the server", async () => {
    at("/admin/people");
    const srv = await start(session("member"), {});
    expect(screen.getByText("Your role cannot use this page.")).toBeTruthy();
    expect(srv.calls.filter((c) => c.url.startsWith("/admin/api/v1/members"))).toHaveLength(0);
  });

  it("signs out with a real form carrying the CSRF token", async () => {
    at("/admin/tokens");
    await start(session("member"), { "GET /admin/api/v1/me/tokens": { tokens: [] } });
    const form = screen.getByRole("button", { name: "Sign out" }).closest("form")!;
    expect(form.getAttribute("action")).toBe("/admin/signout");
    expect(form.getAttribute("method")).toBe("post");
    expect((form.querySelector('input[name="csrf"]') as HTMLInputElement).value).toBe("csrf-token-123");
  });
});

describe("servers", () => {
  it("lists servers and adds one, then opens it", async () => {
    at("/admin/servers");
    const srv = await start(session("owner"), {
      "GET /admin/api/v1/servers": { servers: [{ alias: "deepwiki", status: "up", target: "https://mcp.deepwiki.com/mcp", items: { tool: 3 }, pending_review: 2 }] },
      "POST /admin/api/v1/servers": () => [201, { server: { alias: "linear" } }],
      "GET /admin/api/v1/servers/linear/pins": { pins: [] },
    });
    await screen.findByText("deepwiki");
    expect(screen.getByText("2 to review")).toBeTruthy();
    fireEvent.input(screen.getByPlaceholderText("deepwiki"), { target: { value: "linear" } });
    fireEvent.input(screen.getByPlaceholderText("https://mcp.example.com/mcp"), { target: { value: "https://mcp.linear.app/mcp" } });
    fireEvent.click(screen.getByLabelText(/A shared API key/));
    fireEvent.input(screen.getByPlaceholderText("Bearer sk-…"), { target: { value: "Bearer s3cret" } });
    fireEvent.click(screen.getByRole("button", { name: "Add server" }));
    await waitFor(() => expect(window.location.pathname).toBe("/admin/servers/linear"));
    expect(srv.writes()[0]!.body).toEqual({ alias: "linear", transport: "http", url: "https://mcp.linear.app/mcp", headers: { Authorization: "Bearer s3cret" } });
    expect(screen.getByRole("status").textContent).toContain("Server added");
  });

  it("shows what an upstream says as text, and accepts the ticked tools", async () => {
    at("/admin/servers/deepwiki");
    const pins = [
      { kind: "tool", name: "ask", hash: "h1", status: "pending", definition: { description: evil + "\nsecond line" } },
      { kind: "tool", name: "wipe", hash: "h2", status: "pending", definition: { description: "deletes" } },
      { kind: "tool", name: "read", hash: "h3", status: "accepted", definition: { description: "reads" } },
    ];
    const srv = await start(session("owner"), {
      "GET /admin/api/v1/servers": { servers: [{ alias: "deepwiki", status: "up", target: "t", transport: "http", enabled: true, items: { tool: 1 }, pending_review: 2 }] },
      "GET /admin/api/v1/servers/deepwiki/pins": { pins },
      "POST /admin/api/v1/servers/deepwiki/pins:accept": { accepted: 1 },
    });
    await screen.findByText(evil);
    expect(document.querySelector("img")).toBeNull();
    expect((window as unknown as { pwned?: number }).pwned).toBeUndefined();
    expect(screen.queryByText(/second line/)).toBeNull();
    fireEvent.click(screen.getByLabelText("Accept wipe"));
    fireEvent.click(screen.getByRole("button", { name: "Accept 1 tool" }));
    await waitFor(() => expect(srv.writes()).toHaveLength(1));
    expect(srv.writes()[0]!.body).toEqual({ pins: [{ kind: "tool", hash: "h1", name: "ask" }] });
  });

  it("deletes a server only after its name is typed", async () => {
    at("/admin/servers/deepwiki");
    const srv = await start(session("owner"), {
      "GET /admin/api/v1/servers": { servers: [{ alias: "deepwiki", status: "up", target: "t", enabled: true, items: {}, pending_review: 0 }] },
      "GET /admin/api/v1/servers/deepwiki/pins": { pins: [] },
      "DELETE /admin/api/v1/servers/deepwiki": { deleted: "deepwiki" },
    });
    const del = (await screen.findByRole("button", { name: "Delete server" })) as HTMLButtonElement;
    expect(del.disabled).toBe(true);
    fireEvent.input(document.querySelector('input[name="confirm"]')!, { target: { value: "deepwiki" } });
    expect(del.disabled).toBe(false);
    fireEvent.click(del);
    await waitFor(() => expect(window.location.pathname).toBe("/admin/servers"));
    expect(srv.writes().map((c) => c.method + " " + c.url)).toEqual(["DELETE /admin/api/v1/servers/deepwiki"]);
  });
});

describe("tokens", () => {
  it("shows a new token once, with the command for Claude Code", async () => {
    at("/admin/tokens");
    await start(session("member"), {
      "GET /admin/api/v1/me/tokens": { tokens: [] },
      "GET /admin/api/me/connections": { connections: [], can_connect: true },
      "POST /admin/api/v1/me/tokens": () => [201, { id: "t1", token: "privo_pat_abc_def", name: "laptop", kind: "gateway", role: "member", person: "ada", expires_at: "2027-01-01T00:00:00Z" }],
    });
    fireEvent.input(screen.getByLabelText("Name of the AI client"), { target: { value: "laptop" } });
    fireEvent.click(screen.getByRole("button", { name: "Create token" }));
    await screen.findByText("privo_pat_abc_def");
    expect(screen.getByText('claude mcp add --transport http privo https://privo.example/mcp --header "Authorization: Bearer privo_pat_abc_def"')).toBeTruthy();
    navigate("/admin/connections");
    await screen.findByText("Your connections");
    navigate("/admin/tokens");
    await waitFor(() => expect(screen.queryByText("privo_pat_abc_def")).toBeNull());
  });
});

describe("approvals", () => {
  it("shows the arguments as text and approves", async () => {
    at("/admin/approvals");
    const srv = await start(session("approver"), {
      "GET /admin/api/v1/approvals?status=pending": {
        approvals: [{ id: "a1", status: "pending", tool: "delete_repo", server: "github", requester: "laptop", requester_person: "grace", reason: "", arguments: { repo: evil } }],
      },
      "POST /admin/api/v1/approvals/a1/approve": { approval: { id: "a1" } },
    });
    await screen.findByText("delete_repo");
    expect(document.querySelector("pre")!.textContent).toBe(JSON.stringify({ repo: evil }, null, 2));
    expect(document.querySelector("img")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Approve this call" }));
    await waitFor(() => expect(srv.writes().map((c) => c.url)).toEqual(["/admin/api/v1/approvals/a1/approve"]));
    await screen.findByText("Approved. The call can go ahead.");
  });

  it("offers only Deny when the arguments cannot be shown, and shows the API's refusal", async () => {
    at("/admin/approvals");
    await start(session("approver"), {
      "GET /admin/api/v1/approvals?status=pending": {
        approvals: [{ id: "a2", status: "pending", tool: "t", server: "s", requester: "r", requester_person: "ada", reason: "", arguments_error: "cannot decrypt" }],
      },
      "POST /admin/api/v1/approvals/a2/deny": () => [403, { error: "you cannot decide your own request" }],
    });
    await screen.findByText(/can only be denied/);
    expect(screen.queryByRole("button", { name: "Approve this call" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Deny" }));
    await screen.findByText("you cannot decide your own request");
  });
});

describe("people", () => {
  it("creates an invite and shows its link once", async () => {
    at("/admin/people");
    const srv = await start(session("owner"), {
      "GET /admin/api/v1/members": { members: [{ person: "ada", name: "Ada Lovelace", role: "owner", github_login: "ada-gh", removed: false }] },
      "POST /admin/api/v1/invites": () => [201, { invite: { person: "bob", role: "member", url: "https://privo.example/admin/invite/privo_invite_x", expires_at: "2026-11-06T00:00:00Z" } }],
    });
    await screen.findByText("(you)");
    fireEvent.input(document.querySelector('input[name="person"]')!, { target: { value: " bob " } });
    fireEvent.click(screen.getByRole("button", { name: "Create invite link" }));
    await screen.findByText("https://privo.example/admin/invite/privo_invite_x");
    expect(srv.writes()[0]!.body).toEqual({ person: "bob", role: "member" });
  });
});
