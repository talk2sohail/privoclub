import { describe, expect, it } from "vitest";
import { assertTargetSafety, loadTarget } from "./config.js";
import { FixtureManager } from "./privo/fixture-manager.js";
import { PrivoAdminApi } from "./privo/admin-api.js";

describe("target config & safety guards", () => {
  it("blocks release and explore suites against prod by default", () => {
    const prod = loadTarget("prod");
    expect(prod.allowMutations).toBe(false);
    expect(() => assertTargetSafety(prod, "smoke")).not.toThrow();
    expect(() => assertTargetSafety(prod, "release")).toThrow(/Refusing to run mutating suite/);
    expect(() => assertTargetSafety(prod, "explore")).toThrow(/Refusing to run mutating suite/);
  });

  it("allows release and explore suites against staging", () => {
    const staging = loadTarget("staging");
    expect(staging.allowMutations).toBe(true);
    expect(() => assertTargetSafety(staging, "smoke")).not.toThrow();
    expect(() => assertTargetSafety(staging, "release")).not.toThrow();
    expect(() => assertTargetSafety(staging, "explore")).not.toThrow();
  });

  it("generates Privo-valid server aliases matching ^[a-z][a-z0-9-]{1,15}$", () => {
    const aliasRe = /^[a-z][a-z0-9-]{1,15}$/;
    const fm = new FixtureManager(new PrivoAdminApi("https://staging.privo.club", "tok"), "a3f1");
    for (const code of ["dw", "c7", "cf", "gh", "ref", "longshortcode123"]) {
      const alias = fm.serverAlias(code);
      expect(alias).toMatch(aliasRe);
    }
  });
});
