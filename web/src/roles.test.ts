import { describe, expect, it } from "vitest";
import { pickRole, roleText } from "./roles";

// ADR 0025: owner or admin alone, or any of member, approver and auditor.
describe("pickRole", () => {
  it("combines member, approver and auditor", () => {
    expect(pickRole(["member"], "approver", true)).toEqual(["approver", "member"]);
    expect(pickRole(["approver", "member"], "auditor", true)).toEqual(["approver", "auditor", "member"]);
    expect(pickRole(["approver", "member"], "approver", false)).toEqual(["member"]);
  });
  it("keeps owner and admin alone", () => {
    expect(pickRole(["approver", "member"], "admin", true)).toEqual(["admin"]);
    expect(pickRole(["admin"], "member", true)).toEqual(["member"]);
    expect(pickRole(["owner"], "admin", true)).toEqual(["admin"]);
  });
  it("names the set", () => {
    expect(roleText(["approver", "member"])).toBe("approver, member");
    expect(roleText([])).toBe("no role");
  });
});
