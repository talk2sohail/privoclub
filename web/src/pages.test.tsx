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
    expect(nav.textContent).toContain("Start");
    expect(nav.textContent).toContain("Tokens");
    expect(nav.textContent).toContain("Connections");
    for (const hidden of ["Approvals", "Servers", "Teams", "Rules", "People", "Activity"]) expect(nav.textContent).not.toContain(hidden);
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
      "GET /admin/api/v1/teams": { teams: [{ slug: "everyone", name: "Everyone", description: "", profile: "", built_in: true, members: [], servers: [] }], profiles: [] },
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
    expect(srv.writes()[0]!.body).toEqual({ alias: "linear", transport: "http", url: "https://mcp.linear.app/mcp", teams: ["everyone"], headers: { Authorization: "Bearer s3cret" } });
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

describe("all tokens", () => {
  // F07 R-01 / F14 R-02: an API token acts for a service, never for a member.
  it("tells a member's own token from a service's and asks API tokens for a service", async () => {
    at("/admin/tokens");
    await start(session("owner"), {
      "GET /admin/api/v1/me/tokens": { tokens: [] },
      "GET /admin/api/me/connections": { connections: [], can_connect: true },
      "GET /admin/api/v1/tokens": {
        tokens: [
          { id: "t1", name: "laptop", kind: "gateway", role: "member", person: "ada", personal: true, expires_at: "2027-01-01T00:00:00Z" },
          { id: "t2", name: "ci", kind: "control", role: "auditor", person: "ci-bot", personal: false, expires_at: "2027-01-01T00:00:00Z" },
        ],
      },
    });
    await screen.findByText("laptop");
    expect(screen.getByText("own token")).toBeTruthy();
    expect(screen.getByText("service")).toBeTruthy();
    expect(screen.queryByLabelText(/^Person/)).toBeNull();
    expect(screen.getByLabelText(/^Service/)).toBeTruthy();
  });
});

describe("activity", () => {
  it("names whose account a call used, without its id", async () => {
    at("/admin/activity");
    await start(session("owner"), {
      "GET /admin/api/v1/audit/events?limit=100": {
        events: [{ time: "2026-11-18T10:00:00Z", actor: "laptop", server: "linear", item: "whoami", action: "tools/call", decision: "allow", outcome: "ok", approved_by: "", account: "ada (user u-1)" }],
      },
    });
    await screen.findByText("whoami");
    expect(screen.getByText("as ada")).toBeTruthy();
    expect(screen.queryByText(/u-1/)).toBeNull();
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

const teamsAnswer = {
  teams: [
    { slug: "everyone", name: "Everyone", description: "Every member of the organisation.", profile: "", built_in: true, members: [], servers: ["notion"] },
    { slug: "designers", name: "Designers", description: "", profile: "designers", members: [{ person: "mia", name: "Mia" }], servers: [] },
  ],
  profiles: [
    { id: "developers", name: "Developers", description: "Code." },
    { id: "designers", name: "Designers", description: "Design." },
    { id: "everyone", name: "Everyone", description: "All." },
  ],
};

const catalogAnswer = {
  profiles: teamsAnswer.profiles,
  callback_url: "https://privo.example/oauth/callback",
  oauth_available: true,
  entries: [
    { id: "figma", name: "Figma", description: "Design files.", alias: "figma", url: "https://mcp.figma.com/mcp", transport: "http", auth: "oauth", registration: "automatic", profiles: ["designers"], checked_on: "2026-11-03" },
    { id: "github", name: "GitHub", description: "Code.", alias: "github", url: "https://api.githubcopilot.com/mcp/", transport: "http", auth: "oauth", registration: "own_app", app_url: "https://github.com/settings/applications/new", app_help: "Create an OAuth App.", scopes: ["repo"], profiles: ["developers"], checked_on: "2026-11-03" },
    { id: "notion", name: "Notion", description: "Docs.", alias: "notion", url: "https://mcp.notion.com/mcp", transport: "http", auth: "oauth", registration: "automatic", profiles: ["everyone"], checked_on: "2026-11-03", installed: "notion" },
  ],
};

describe("start", () => {
  it("shows the person's teams and servers, the connect link, and sets up a client with a new token", async () => {
    at("/admin/start");
    const srv = await start(session("member"), {
      "GET /admin/api/v1/me/tools": {
        teams: [{ slug: "designers", name: "Designers" }, { slug: "everyone", name: "Everyone" }],
        servers: [
          { alias: "figma", name: "Figma", description: evil, status: "up", tools: 4, auth: "oauth", connected: false, connect_url: "https://privo.example/connect/figma" },
          { alias: "deepwiki", name: "deepwiki", status: "up", tools: 2 },
        ],
      },
      "GET /admin/api/v1/me/tokens": { tokens: [] },
      "POST /admin/api/v1/me/tokens": () => [201, { id: "t1", token: "privo_pat_abc_def", name: "cursor", kind: "gateway", role: "member", person: "ada", expires_at: "2027-01-01T00:00:00Z" }],
    });
    await screen.findByText("Figma");
    expect(screen.getByText("Designers")).toBeTruthy();
    expect(screen.getByText(evil)).toBeTruthy();
    expect(document.querySelector("img")).toBeNull();
    expect(screen.getByRole("link", { name: "Connect" }).getAttribute("href")).toBe("https://privo.example/connect/figma");
    expect(screen.getByText("not needed")).toBeTruthy();
    // Before a token exists, the setup shows a placeholder.
    expect(screen.getByText(/Bearer <your token>/)).toBeTruthy();
    fireEvent.input(screen.getByLabelText("Name of the AI client"), { target: { value: "cursor" } });
    fireEvent.click(screen.getByRole("button", { name: "Create token" }));
    await screen.findByText("privo_pat_abc_def");
    fireEvent.click(screen.getByRole("tab", { name: "Cursor" }));
    const snippet = [...document.querySelectorAll("pre")].map((p) => p.textContent ?? "").find((t) => t.includes("mcpServers"))!;
    expect(JSON.parse(snippet)).toEqual({ mcpServers: { privo: { url: "https://privo.example/mcp", headers: { Authorization: "Bearer privo_pat_abc_def" } } } });
    expect(srv.writes().map((c) => c.method + " " + c.url)).toEqual(["POST /admin/api/v1/me/tokens"]);
  });

  it("says so when no server is shared with the person", async () => {
    at("/admin/start");
    await start(session("member"), { "GET /admin/api/v1/me/tools": { teams: [{ slug: "everyone", name: "Everyone" }], servers: [] }, "GET /admin/api/v1/me/tokens": { tokens: [] } });
    await screen.findByText(/No servers are shared with your teams yet/);
  });
});

describe("teams", () => {
  it("creates a team from a profile, opens it and adds a member", async () => {
    at("/admin/teams");
    const srv = await start(session("owner"), {
      "GET /admin/api/v1/teams": teamsAnswer,
      "GET /admin/api/v1/members": {
        members: [
          { person: "mia", name: "Mia", role: "member", github_login: "mia", removed: false },
          { person: "dev", name: "Dev", role: "member", github_login: "dev", removed: false },
          { person: "gone", name: "Gone", role: "member", github_login: "gone", removed: true },
        ],
      },
      "GET /admin/api/v1/servers": { servers: [{ alias: "notion", status: "up", target: "", items: {}, pending_review: 0, teams: ["everyone"] }] },
      "GET /admin/api/v1/catalog": catalogAnswer,
      "POST /admin/api/v1/teams": () => [201, { team: { slug: "developers", name: "Developers", description: "", profile: "developers", members: [], servers: [] } }],
      "POST /admin/api/v1/teams/designers/members": { team: "designers", added: "dev" },
    });
    await screen.findByText("every member");
    // Everyone cannot be offered as a profile to start from.
    expect(screen.queryByRole("radio", { name: /^Everyone/ })).toBeNull();
    fireEvent.click(screen.getByRole("radio", { name: /Developers/ }));
    expect((document.querySelector('input[name="name"]') as HTMLInputElement).value).toBe("Developers");
    fireEvent.click(screen.getByRole("button", { name: "Create team" }));
    await waitFor(() => expect(srv.writes()[0]?.body).toEqual({ name: "Developers", description: "", profile: "developers" }));
    await screen.findByText("Team created. Add its people and servers below.");

    const openButtons = screen.getAllByRole("button", { name: "Open" });
    fireEvent.click(openButtons[1]!);
    const detail = await screen.findByRole("region", { name: "Designers" });
    const select = detail.querySelector('select[name="person"]') as HTMLSelectElement;
    expect([...select.options].map((o) => o.value)).toEqual(["dev"]); // not mia (already in), not a removed member
    fireEvent.click(screen.getByRole("button", { name: "Add to team" }));
    await waitFor(() => expect(srv.writes()[1]).toMatchObject({ method: "POST", url: "/admin/api/v1/teams/designers/members", body: { person: "dev" } }));
    // The designers profile suggests Figma from the catalog.
    expect(await screen.findByText("Figma")).toBeTruthy();
  });
});

describe("catalog", () => {
  it("adds a server from the catalog for the chosen teams, with its OAuth app where one is needed", async () => {
    at("/admin/servers");
    const srv = await start(session("owner"), {
      "GET /admin/api/v1/servers": { servers: [] },
      "GET /admin/api/v1/teams": teamsAnswer,
      "GET /admin/api/v1/catalog": catalogAnswer,
      "POST /admin/api/v1/servers": () => [201, { server: { alias: "github" } }],
      "GET /admin/api/v1/servers/github/pins": { pins: [] },
    });
    await screen.findByText("GitHub");
    expect(screen.getByRole("link", { name: "added as notion" })).toBeTruthy();
    fireEvent.click(screen.getByRole("tab", { name: "Developers" }));
    expect(screen.queryByText("Figma")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Add" }));
    const form = await screen.findByRole("form", { name: "Add GitHub" });
    expect(form.textContent).toContain("https://privo.example/oauth/callback");
    // Give it to the designers instead of everyone.
    const boxes = form.querySelectorAll('input[type="checkbox"]');
    fireEvent.click(boxes[0]!);
    fireEvent.click(boxes[1]!);
    fireEvent.input(form.querySelector('input[name="client_id"]')!, { target: { value: "Iv1.abc" } });
    fireEvent.input(form.querySelector('input[name="client_secret"]')!, { target: { value: "s3cret" } });
    fireEvent.click(screen.getByRole("button", { name: "Add GitHub" }));
    await waitFor(() => expect(window.location.pathname).toBe("/admin/servers/github"));
    expect(srv.writes()[0]!.body).toEqual({
      alias: "github",
      transport: "http",
      url: "https://api.githubcopilot.com/mcp/",
      teams: ["designers"],
      oauth: { client_id: "Iv1.abc", client_secret: "s3cret", scopes: ["repo"] },
    });
  });

  it("warns when nobody would be able to use the server", async () => {
    at("/admin/servers");
    await start(session("owner"), { "GET /admin/api/v1/servers": { servers: [] }, "GET /admin/api/v1/teams": teamsAnswer, "GET /admin/api/v1/catalog": catalogAnswer });
    await screen.findByText("Figma");
    fireEvent.click(screen.getAllByRole("button", { name: "Add" })[0]!);
    const form = await screen.findByRole("form", { name: "Add Figma" });
    fireEvent.click(form.querySelector('input[type="checkbox"]')!); // untick everyone
    expect(form.textContent).toContain("Nobody will be able to use this server");
  });
});

describe("server access", () => {
  it("saves who can use a server", async () => {
    at("/admin/servers/notion");
    const srv = await start(session("owner"), {
      "GET /admin/api/v1/servers": { servers: [{ alias: "notion", status: "up", target: "https://mcp.notion.com/mcp", items: { tool: 1 }, pending_review: 0, teams: ["everyone"] }] },
      "GET /admin/api/v1/servers/notion/pins": { pins: [] },
      "GET /admin/api/v1/teams": teamsAnswer,
      "PATCH /admin/api/v1/servers/notion": { server: { alias: "notion", teams: ["designers"] } },
    });
    const save = (await screen.findByRole("button", { name: "Save" })) as HTMLButtonElement;
    const card = save.closest("section")!;
    expect(save.disabled).toBe(true);
    const boxes = card.querySelectorAll('input[type="checkbox"]');
    fireEvent.click(boxes[1]!); // designers
    fireEvent.click(boxes[0]!); // not everyone
    expect(save.disabled).toBe(false);
    fireEvent.click(save);
    await waitFor(() => expect(srv.writes()[0]).toMatchObject({ method: "PATCH", url: "/admin/api/v1/servers/notion", body: { teams: ["designers"] } }));
  });
});

describe("review fixes (F26)", () => {
  it("warns about a server nobody can use", async () => {
    at("/admin/servers");
    await start(session("owner"), {
      "GET /admin/api/v1/servers": { servers: [{ alias: "orphan", status: "up", target: "", items: {}, pending_review: 0, teams: [] }] },
      "GET /admin/api/v1/teams": teamsAnswer,
      "GET /admin/api/v1/catalog": catalogAnswer,
    });
    const cell = await screen.findByText("nobody");
    expect(cell.className).toBe("warn-text");
  });

  it("does not add a server while the teams cannot be read", async () => {
    at("/admin/servers");
    const srv = await start(session("owner"), {
      "GET /admin/api/v1/servers": { servers: [] },
      "GET /admin/api/v1/teams": () => [500, { error: "teams are down" }],
      "GET /admin/api/v1/catalog": catalogAnswer,
    });
    await screen.findByText("teams are down");
    expect((screen.getByRole("button", { name: "Add server" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getAllByRole("button", { name: "Add" })[0]!);
    expect((screen.getByRole("button", { name: "Add Figma" }) as HTMLButtonElement).disabled).toBe(true);
    expect(srv.writes()).toHaveLength(0);
  });
});
