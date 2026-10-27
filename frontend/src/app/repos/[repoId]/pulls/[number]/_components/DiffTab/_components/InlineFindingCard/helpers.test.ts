import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
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

  it("code is only what GitLab surely renders as code: a backtick pair across a blank line is prose (security r1 HIGH-1)", () => {
    expect(sanitizeForGitLab("See `\n\n@all ![t](https://evil/t.png)\n\n` end")).toBe(
      `See \`\n\n@${Z}all !${Z}[t](https://evil/t.png)\n\n\` end`,
    );
    expect(sanitizeForGitLab("- ```\n@victim ![p](https://evil/p.png)\n```")).toBe(
      `- \`\`\`\n@${Z}victim !${Z}[p](https://evil/p.png)\n\`\`\``,
    );
    expect(sanitizeForGitLab("~~~\n`\n~~~\n@victim <img src=x>\n`")).toBe(
      `~~~\n\`\n~~~\n@${Z}victim &lt;img src=x&gt;\n\``,
    );
    expect(sanitizeForGitLab("    `\n\n@victim `")).toBe(`    \`\n\n@${Z}victim \``);
    expect(sanitizeForGitLab("\\`@victim <b>x</b>\\`")).toBe(`\\\`@${Z}victim &lt;b&gt;x&lt;/b&gt;\\\``);
  });

  it("a closed fence at column 0 is code, an unclosed or list-nested or indented one is prose", () => {
    expect(sanitizeForGitLab("```\n@a <b> ![c]\n```")).toBe("```\n@a <b> ![c]\n```");
    expect(sanitizeForGitLab("~~~\n<b>\n~~~")).toBe("~~~\n<b>\n~~~");
    expect(sanitizeForGitLab("```\n@a")).toBe(`\`\`\`\n@${Z}a`);
    expect(sanitizeForGitLab("- x\n  ```\n@a\n  ```")).toBe(`- x\n  \`\`\`\n@${Z}a\n  \`\`\``);
    expect(sanitizeForGitLab("    ```\n@a\n    ```")).toBe(`    \`\`\`\n@${Z}a\n    \`\`\``);
    expect(sanitizeForGitLab("| a |\n|---|\n| `x | @all` |")).toBe(`| a |\n|---|\n| \`x | @${Z}all\` |`);
  });

  it("an entity that decodes to @ < ! [ loses its ampersand (D-new-2); other references stay", () => {
    expect(sanitizeForGitLab("&#64;u &#x40;u &commat;u &#X40;u &CommAt;u")).toBe(
      "&amp;#64;u &amp;#x40;u &amp;commat;u &amp;#X40;u &amp;CommAt;u",
    );
    expect(sanitizeForGitLab("&#60;img &#x3c;img &LT;img &#33;[x](u) &excl; &lsqb; &lbrack; &#0000064;")).toBe(
      "&amp;#60;img &amp;#x3c;img &amp;LT;img &amp;#33;[x](u) &amp;excl; &amp;lsqb; &amp;lbrack; &amp;#0000064;",
    );
    expect(sanitizeForGitLab("&lt;img&gt; &amp;#64; &#65; &#64 &copy; `&#64;`")).toBe("&lt;img&gt; &amp;#64; &#65; &#64 &copy; `&#64;`");
  });

  it("matches the server's EC15 corpus for every case that does not hold the server-only sub line or marker", () => {
    const corpusPath = path.resolve(
      path.dirname(fileURLToPath(import.meta.url)),
      "../../../../../../../../../../../backend/internal/platform/gitlab/notebody/testdata/ec15.json",
    );
    const corpus = JSON.parse(readFileSync(corpusPath, "utf8")) as { name: string; in: string; want: string }[];
    const comparable = corpus.filter((c) => !c.in.includes("<sub>DevDigest") && !c.in.includes("devdigest-finding"));
    expect(comparable.length).toBeGreaterThan(80);
    for (const c of comparable) expect({ name: c.name, got: sanitizeForGitLab(c.in) }).toEqual({ name: c.name, got: c.want });
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
