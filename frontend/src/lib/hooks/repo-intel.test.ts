import { describe, expect, it } from "vitest";
import type { RepoIndexState } from "@devdigest/shared";
import { hasNoSourceFiles } from "./repo-intel";

const state = (over: Partial<RepoIndexState>): RepoIndexState => ({
  repoId: "r1",
  status: "partial",
  filesIndexed: 0,
  filesSkipped: 0,
  durationMs: 1,
  reason: "no_files",
  lastIndexedSha: "abc",
  indexerVersion: 1,
  updatedAt: "2026-10-01T00:00:00.000Z",
  ...over,
});

describe("hasNoSourceFiles (FR28)", () => {
  it("is true only for an indexed state with reason no_files and no indexed files", () => {
    expect(hasNoSourceFiles(state({}))).toBe(true);
  });

  it.each([
    ["undefined", undefined],
    ["an empty lastIndexedSha (never indexed, EC13)", state({ lastIndexedSha: "" })],
    ["another reason", state({ reason: "budget" })],
    ["no reason", state({ reason: undefined })],
    ["indexed files", state({ filesIndexed: 12 })],
  ])("is false for %s", (_name, s) => {
    expect(hasNoSourceFiles(s)).toBe(false);
  });
});
