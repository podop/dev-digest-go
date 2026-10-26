import { describe, it, expect } from "vitest";
import type { IntentSource } from "@devdigest/shared";
import { sourceHref } from "./helpers";

const GITHUB = { provider: "github", full_name: "acme/api" } as const;
const GITLAB = {
  provider: "gitlab",
  full_name: "acme/platform/payments",
  web_url: "https://git.corp.example/gitlab/acme/platform/payments",
} as const;

const src = (kind: IntentSource["kind"], ref: string): IntentSource => ({ kind, ref, status: "used" }) as IntentSource;

describe("sourceHref", () => {
  it("links a ticket to the repository's issues and a doc to its blob at the head sha, per host", () => {
    expect(sourceHref(src("ticket", "42"), GITHUB, "abc")).toBe("https://github.com/acme/api/issues/42");
    expect(sourceHref(src("doc", "docs/a b.md"), GITHUB, "abc")).toBe("https://github.com/acme/api/blob/abc/docs/a%20b.md");
    expect(sourceHref(src("ticket", "42"), GITLAB, "abc")).toBe(
      "https://git.corp.example/gitlab/acme/platform/payments/-/issues/42",
    );
    expect(sourceHref(src("doc", "docs/a b.md"), GITLAB, "abc")).toBe(
      "https://git.corp.example/gitlab/acme/platform/payments/-/blob/abc/docs/a%20b.md",
    );
  });

  it("is undefined without a repository, a head sha for a doc, a numeric ticket or a linkable kind", () => {
    expect(sourceHref(src("ticket", "42"), null, "abc")).toBeUndefined();
    expect(sourceHref(src("doc", "README.md"), GITHUB, null)).toBeUndefined();
    expect(sourceHref(src("ticket", "ABC-1"), GITHUB, "abc")).toBeUndefined();
    expect(sourceHref(src("title", "x"), GITHUB, "abc")).toBeUndefined();
  });
});
