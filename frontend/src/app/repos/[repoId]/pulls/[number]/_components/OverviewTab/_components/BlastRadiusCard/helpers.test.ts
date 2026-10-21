import { describe, it, expect } from "vitest";
import type { BlastRadius } from "@devdigest/shared";
import { blastStats, callerHref, callerNodeLabel, callerSymbolLabel, degradedReasonKey } from "./helpers";

const caller = (name: string, file: string, line: number) => ({ name, file, line });
const BLAST: BlastRadius = {
  changed_symbols: [
    { name: "a", file: "src/a.ts", kind: "function" },
    { name: "b", file: "src/b.ts", kind: "function" },
    { name: "unused", file: "src/c.ts", kind: "function" },
  ],
  downstream: [
    {
      symbol: "a",
      callers: [caller("x", "src/x.ts", 1), caller("y", "src/y.ts", 2)],
      endpoints_affected: ["GET /x", "POST /y"],
      crons_affected: ["nightly"],
    },
    {
      symbol: "b",
      callers: [caller("x", "src/x.ts", 1)],
      endpoints_affected: ["GET /x"],
      crons_affected: [],
    },
  ],
  summary: "s",
};

describe("blastStats", () => {
  it("counts changed symbols (also those without callers) and distinct callers / endpoints / crons", () => {
    expect(blastStats(BLAST)).toEqual({ symbols: 3, callers: 2, endpoints: 2, crons: 1 });
  });

  it("is all zero for an empty response", () => {
    expect(blastStats({ changed_symbols: [], downstream: [], summary: "" })).toEqual({
      symbols: 0,
      callers: 0,
      endpoints: 0,
      crons: 0,
    });
  });
});

describe("callerHref", () => {
  it("links the exact line at the head sha and encodes the path", () => {
    expect(callerHref("acme/api", "abc123", caller("x", "src/my file.ts", 23))).toBe(
      "https://github.com/acme/api/blob/abc123/src/my%20file.ts#L23",
    );
  });

  it("is undefined without a repo name or a head sha", () => {
    expect(callerHref(null, "abc", caller("x", "src/x.ts", 1))).toBeUndefined();
    expect(callerHref("acme/api", null, caller("x", "src/x.ts", 1))).toBeUndefined();
  });
});

describe("degradedReasonKey", () => {
  it("passes known reasons through and maps everything else to unknown", () => {
    expect(degradedReasonKey("index_partial")).toBe("index_partial");
    expect(degradedReasonKey("something_new")).toBe("unknown");
    expect(degradedReasonKey(undefined)).toBe("unknown");
  });
});

describe("callerSymbolLabel / callerNodeLabel", () => {
  it("adds () to real symbol names only; a file-name fallback is no symbol", () => {
    expect(callerSymbolLabel(caller("publicRouter", "src/router.ts", 3))).toBe("publicRouter()");
    expect(callerSymbolLabel(caller("Foo.bar", "src/foo.ts", 3))).toBe("Foo.bar()");
    expect(callerSymbolLabel(caller("composition.test.ts", "server/test/composition.test.ts", 9))).toBeNull();
    expect(callerSymbolLabel(caller("main.ts", "main.ts", 1))).toBeNull();
    expect(callerSymbolLabel(caller("server/test/a.ts", "server/test/a.ts", 1))).toBeNull();

    expect(callerNodeLabel(caller("publicRouter", "src/router.ts", 3))).toBe("publicRouter()");
    expect(callerNodeLabel(caller("composition.test.ts", "server/test/composition.test.ts", 9))).toBe("composition.test.ts");
  });
});
