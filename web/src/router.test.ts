import { describe, expect, it } from "vitest";
import { match, navigate, query } from "./router";

describe("router", () => {
  it("matches patterns and decodes parameters", () => {
    expect(match("/admin/servers/:alias", "/admin/servers/linear")).toEqual({ alias: "linear" });
    expect(match("/admin/servers/:alias", "/admin/servers/a%20b?x=1")).toEqual({ alias: "a b" });
    expect(match("/admin/servers/:alias", "/admin/servers/")).toBeNull();
    expect(match("/admin/servers", "/admin/servers/linear")).toBeNull();
    expect(match("/admin/servers/:alias", "/admin/servers/%E0%A4%A")).toBeNull();
  });

  it("reads the query", () => {
    expect(query("/admin/activity?view=admin", "view")).toBe("admin");
    expect(query("/admin/activity", "view")).toBe("");
  });

  it("navigates with the history API", () => {
    navigate("/admin/rules");
    expect(window.location.pathname).toBe("/admin/rules");
  });
});
