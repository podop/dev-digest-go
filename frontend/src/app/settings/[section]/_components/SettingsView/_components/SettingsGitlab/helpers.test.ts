import { describe, it, expect } from "vitest";
import type { GitLabConnection } from "@/lib/types";
import {
  autoTestIds,
  buildCreate,
  buildUpdate,
  canSubmit,
  createTokenUrl,
  emptyForm,
  fieldErrorsFrom,
  hostOfBaseUrl,
  scopesText,
} from "./helpers";

const c = (id: string, token: boolean): GitLabConnection => ({
  id,
  name: id,
  base_url: "https://gitlab.com",
  host: "gitlab.com",
  token_configured: token,
  repo_count: 0,
  created_at: "",
  updated_at: "",
});

describe("helpers", () => {
  it("picks the first 5 connections with a token, in list order", () => {
    const list = ["a", "b", "c", "d", "e", "f", "g", "h"].map((id, i) => c(id, i !== 1));
    expect(autoTestIds(list)).toEqual(["a", "c", "d", "e", "f"]);
  });

  it("builds the token link and the name placeholder from https URLs only", () => {
    expect(hostOfBaseUrl("https://git.corp.example:8443/gl")).toBe("git.corp.example:8443");
    expect(hostOfBaseUrl("https://gitlab.com:443")).toBe("gitlab.com");
    expect(hostOfBaseUrl("http://gitlab.com")).toBeNull();
    expect(hostOfBaseUrl("javascript:alert(1)")).toBeNull();
    expect(createTokenUrl("https://u:p@gitlab.com/gl/?x=1#y")).toBe(
      "https://gitlab.com/gl/-/user_settings/personal_access_tokens?name=DevDigest&scopes=api,read_repository",
    );
    expect(createTokenUrl("ftp://gitlab.com")).toBeNull();
  });

  it("builds write-only bodies", () => {
    const f = { ...emptyForm(), name: " Main ", token: " glpat-x " };
    expect(buildCreate(f)).toEqual({ name: "Main", base_url: "https://gitlab.com", token: "glpat-x" });
    expect(buildCreate(emptyForm())).toEqual({ base_url: "https://gitlab.com" });
    const cur = c("a", true);
    expect(buildUpdate({ ...emptyForm(), name: "a", baseUrl: cur.base_url }, cur)).toEqual({ name: "a" });
    expect(buildUpdate({ ...emptyForm(), name: "", baseUrl: cur.base_url, removeToken: true }, cur)).toEqual({ name: "", token: null });
    expect(buildUpdate({ ...emptyForm(), name: "a", baseUrl: cur.base_url, token: "t", removeToken: true }, cur)).toEqual({ name: "a", token: "t" });
  });

  it("disables Save on an empty base URL or a too long name", () => {
    expect(canSubmit(emptyForm())).toBe(true);
    expect(canSubmit({ ...emptyForm(), baseUrl: "  " })).toBe(false);
    expect(canSubmit({ ...emptyForm(), name: "x".repeat(81) })).toBe(false);
  });

  it("maps server 422 details by params.issue.path, then instancePath, then path", () => {
    const out = fieldErrorsFrom([
      { instancePath: "/token", message: "no whitespace", params: { issue: { path: ["token"] } } },
      { instancePath: "/base_url", message: "must be https" },
      { path: ["name"], message: "too long" },
      { instancePath: "/other", message: "odd" },
      { message: "" },
    ]);
    expect(out).toEqual({ fields: { token: "no whitespace", base_url: "must be https", name: "too long" }, rest: ["odd"] });
    expect(fieldErrorsFrom(undefined)).toEqual({ fields: {}, rest: [] });
  });

  it("joins scopes with a middle dot", () => {
    expect(scopesText(["api", "read_repository"])).toBe("api · read_repository");
  });
});
