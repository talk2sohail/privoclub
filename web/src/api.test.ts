import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, api, loadSession, nav } from "./api";
import { fakeServer, session } from "./test/fake";

afterEach(() => vi.unstubAllGlobals());

describe("api", () => {
  it("sends the session's CSRF token on every call, and JSON when there is a body", async () => {
    const srv = fakeServer({ "GET /admin/api/session": session("owner"), "POST /admin/api/v1/policies": () => [201, { policy: { id: "p1" } }] });
    await loadSession();
    const out = await api<{ policy: { id: string } }>("POST", "/v1/policies", { server: "*", action: "ask" });
    expect(out.policy.id).toBe("p1");
    const post = srv.writes()[0]!;
    expect(post.headers["X-Privo-CSRF"]).toBe("csrf-token-123");
    expect(post.headers["Content-Type"]).toBe("application/json");
    expect(post.body).toEqual({ server: "*", action: "ask" });
  });

  it("does not send a body or a content type without one", async () => {
    const srv = fakeServer({ "GET /admin/api/session": session("owner"), "POST /admin/api/v1/approvals/a1/deny": {} });
    await loadSession();
    await api("POST", "/v1/approvals/a1/deny");
    expect(srv.writes()[0]!.headers["Content-Type"]).toBeUndefined();
  });

  it("rejects with the server's own message", async () => {
    fakeServer({ "DELETE /admin/api/v1/servers/x": () => [409, { error: '"boss" is the last owner' }] });
    await expect(api("DELETE", "/v1/servers/x")).rejects.toEqual(new ApiError(409, '"boss" is the last owner'));
  });

  it("goes to sign-in when the session has ended", async () => {
    fakeServer({ "GET /admin/api/v1/servers": () => [401, { error: "not signed in" }] });
    const signIn = vi.spyOn(nav, "signIn").mockImplementation(() => {});
    await expect(api("GET", "/v1/servers")).rejects.toMatchObject({ status: 401 });
    expect(signIn).toHaveBeenCalledOnce();
  });

  it("says so when the server cannot be reached", async () => {
    vi.stubGlobal("fetch", () => Promise.reject(new TypeError("Failed to fetch")));
    await expect(api("GET", "/v1/servers")).rejects.toMatchObject({ status: 0, message: expect.stringContaining("could not be reached") });
  });
});
