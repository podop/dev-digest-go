import { describe, it, expect } from "vitest";
import type { FindingRecord, PrReviewComment } from "@devdigest/shared";
import {
  findPublishedComment,
  findingCommentBody,
  findingCommentInput,
  findingMarker,
  sanitizeForGitHub,
  sanitizeForGitLab,
} from "./helpers";

const FINDING: FindingRecord = {
  id: "f1",
  severity: "CRITICAL",
  category: "bug",
  title: "PUT route breaks the API contract",
  file: "internal/hsm/routes.go",
  start_line: 24,
  end_line: 26,
  rationale: "  The path moved from `/:id` to `/int/:id`.  ",
  suggestion: "Revert the route change.",
  confidence: 0.904,
  kind: "finding",
  trifecta_components: null,
  evidence: null,
  review_id: "r1",
  accepted_at: null,
  dismissed_at: null,
};

const comment = (id: number, body: string) => ({ id, body, html_url: `https://gh/c/${id}` }) as PrReviewComment;

describe("findingCommentBody", () => {
  it("renders header, rationale, suggested fix, confidence and the hidden marker", () => {
    expect(findingCommentBody(FINDING)).toBe(
      [
        "**[CRITICAL · bug] PUT route breaks the API contract**",
        "The path moved from `/:id` to `/int/:id`.",
        "**Suggested fix**\n\nRevert the route change.",
        "<sub>DevDigest · 90% confidence</sub>",
        "<!-- devdigest-finding:f1 -->",
      ].join("\n\n"),
    );
  });

  it("omits the suggested-fix section when there is none", () => {
    expect(findingCommentBody({ ...FINDING, suggestion: null })).not.toContain("Suggested fix");
    expect(findingCommentBody({ ...FINDING, suggestion: "  " })).not.toContain("Suggested fix");
  });
});

describe("sanitizeForGitHub", () => {
  it("turns raw HTML into text and defuses @-mentions", () => {
    expect(sanitizeForGitHub('<img src="https://x/t.png"> ping @octocat')).toBe(
      '&lt;img src="https://x/t.png"&gt; ping @\u200Boctocat',
    );
  });

  it("keeps inline code and fenced blocks verbatim", () => {
    expect(sanitizeForGitHub("use `Map<K, V>` or ``a`<b>`` @x")).toBe("use `Map<K, V>` or ``a`<b>`` @\u200Bx");
    expect(sanitizeForGitHub("```ts\nconst a: Array<string> = [];\n```")).toBe("```ts\nconst a: Array<string> = [];\n```");
  });

  it("escapes after a backtick run that is never closed", () => {
    expect(sanitizeForGitHub("a ` <b> `` <i>")).toBe("a ` &lt;b&gt; `` &lt;i&gt;");
  });

  it("an unclosed <!-- in the rationale cannot hide the signature and marker", () => {
    const body = findingCommentBody({ ...FINDING, rationale: "looks fine <!-- @admin" });
    expect(body).toContain("looks fine &lt;!-- @\u200Badmin");
    expect(body.indexOf("<!--")).toBe(body.indexOf(findingMarker("f1")));
  });
});

describe("findingCommentInput", () => {
  it("anchors on the finding's first line, new side", () => {
    expect(findingCommentInput(FINDING)).toMatchObject({ path: "internal/hsm/routes.go", line: 24, side: "RIGHT" });
  });
});

describe("findPublishedComment", () => {
  it("matches only the exact finding id", () => {
    const comments = [comment(1, `x ${findingMarker("f10")}`), comment(2, `y ${findingMarker("f1")}`)];
    expect(findPublishedComment(comments, "f1")?.id).toBe(2);
    expect(findPublishedComment(comments, "f2")).toBeUndefined();
    expect(findPublishedComment(undefined, "f1")).toBeUndefined();
  });
});

const Z = "\u200b";

describe("sanitizeForGitLab", () => {
  it("keeps the GitHub rules: HTML as text, mention defused, code spans verbatim", () => {
    expect(sanitizeForGitLab("<b>hi</b> @octocat `Map<K, V>`")).toBe(`&lt;b&gt;hi&lt;/b&gt; @${Z}octocat \`Map<K, V>\``);
  });

  it("FR15: puts U+200B before a leading slash behind blanks, quotes and list markers", () => {
    expect(sanitizeForGitLab("/approve")).toBe(`${Z}/approve`);
    // `>` outside code is HTML-escaped by the GitHub rule first, so the quote prefix is exercised inside a code span below.
    expect(sanitizeForGitLab("  - 1. /merge now")).toBe(`  - 1. ${Z}/merge now`);
    expect(sanitizeForGitLab("\t\ufeff * + 12) /close")).toBe(`\t\ufeff * + 12) ${Z}/close`);
    expect(sanitizeForGitLab("a\n/one\n\n  /two")).toBe(`a\n${Z}/one\n\n  ${Z}/two`);
  });

  it("FR15: a slash that is not at a line start, or a marker without its blank, stays", () => {
    expect(sanitizeForGitLab("see /docs/x")).toBe("see /docs/x");
    expect(sanitizeForGitLab("-/not-a-marker")).toBe("-/not-a-marker");
    expect(sanitizeForGitLab("1234567890. /ten digits is no marker")).toBe("1234567890. /ten digits is no marker");
  });

  it("FR15 applies inside code spans and fences too, behind a `>` quote prefix", () => {
    expect(sanitizeForGitLab("```\n/approve\n> 1. /merge\n```")).toBe(`\`\`\`\n${Z}/approve\n> 1. ${Z}/merge\n\`\`\``);
  });

  it("FR15: CR LF and a lone CR end a line", () => {
    expect(sanitizeForGitLab("a\r\n/x\r/y")).toBe(`a\n${Z}/x\n${Z}/y`);
  });

  it("FR16(c): an image opener outside code becomes text, inside code it stays", () => {
    expect(sanitizeForGitLab("![x](http://a/b.png) `![y]`")).toBe(`!${Z}[x](http://a/b.png) \`![y]\``);
  });

  it("does not insert U+200B a second time for a slash or image opener it already defused", () => {
    const once = sanitizeForGitLab("/a\n![i](u)\n`/c`");
    expect(sanitizeForGitLab(once)).toBe(once);
  });
});

describe("findingCommentBody / findingCommentInput for GitLab", () => {
  const hostile = {
    ...FINDING,
    title: "![t](http://x/t.png) @bob",
    rationale: "/approve\nsee <img src=x>",
    suggestion: "/merge",
  };

  it("sanitizes title, rationale and suggestion the GitLab way and keeps the template and marker", () => {
    const body = findingCommentBody(hostile, { gitlab: true });
    expect(body).toBe(
      [
        `**[CRITICAL · bug] !${Z}[t](http://x/t.png) @${Z}bob**`,
        `${Z}/approve\nsee &lt;img src=x&gt;`,
        `**Suggested fix**\n\n${Z}/merge`,
        "<sub>DevDigest · 90% confidence</sub>",
        "<!-- devdigest-finding:f1 -->",
      ].join("\n\n"),
    );
    expect(findingCommentInput(hostile, { gitlab: true })).toEqual({
      path: "internal/hsm/routes.go",
      line: 24,
      side: "RIGHT",
      body,
    });
  });

  it("builds the GitHub body when the option is absent or false", () => {
    const github = findingCommentBody(hostile);
    expect(github).toBe(findingCommentBody(hostile, { gitlab: false }));
    expect(github).toContain("\n/approve");
    expect(github).toContain("![t]");
    expect(findingCommentInput(hostile).body).toBe(github);
  });
});
