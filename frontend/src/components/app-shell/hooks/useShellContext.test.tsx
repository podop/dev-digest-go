import React from "react";
import { describe, it, expect, afterEach, vi } from "vitest";
import { WORKSPACE_SWITCHER_NAME_MAX } from "@devdigest/shared/constants/workspace";
import { renderHook, cleanup, waitFor, TestProviders, createTestQueryClient } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { makeRepo } from "@/test/context-fixtures";
import { RepoProvider } from "@/lib/repo-context";

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn() }),
  usePathname: () => "/repos/r1/pulls",
}));

import { useShellContext } from "./useShellContext";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const REPO = makeRepo("r1", "payments");
const INFO = (name: string) => ({
  workspaceId: "11111111-1111-4111-8111-111111111111",
  name,
  dataDir: "/data",
  cloneDir: "/data/clones",
  repos: [],
});

function setup(workspace: unknown) {
  mockFetch({ "GET /repos": [REPO], "GET /workspace": workspace as Response });
  const wrapper = ({ children }: { children: React.ReactNode }) => (
    <TestProviders client={createTestQueryClient()}>
      <RepoProvider>{children}</RepoProvider>
    </TestProviders>
  );
  return renderHook(() => useShellContext({ onOpenCommandPalette: () => {} }), { wrapper });
}

describe("the repo switcher subtitle", () => {
  it("reads '<label> · <name>', cuts a long name at 32 code points + '…'", async () => {
    const { result } = setup(INFO("acme · payments"));
    await waitFor(() => expect(result.current.activeRepo?.syncedLabel).toBe("not synced · acme · payments"));
    // The repo list in the dropdown keeps the plain label.
    expect(result.current.repos?.[0]?.syncedLabel).toBe("not synced");
    expect(result.current.activeRepo?.default_branch).toBe("main");
    cleanup();

    // 80 code points, two of them astral (an emoji is one code point, two UTF-16 units).
    const long = `${"😀".repeat(2)}${"x".repeat(78)}`;
    const cut = setup(INFO(long));
    const expected = `not synced · ${"😀".repeat(2)}${"x".repeat(WORKSPACE_SWITCHER_NAME_MAX - 2)}…`;
    await waitFor(() => expect(cut.result.current.activeRepo?.syncedLabel).toBe(expected));
  });

  it("shows no name when loading it fails, and no trailing separator for a blank name", async () => {
    const failed = setup(jsonResponse({ error: { code: "not_found", message: "no" } }, 404));
    await waitFor(() => expect(failed.result.current.activeRepo).not.toBeNull());
    expect(failed.result.current.activeRepo?.syncedLabel).toBe("not synced");
    cleanup();

    const blank = setup(INFO("   "));
    await waitFor(() => expect(blank.result.current.activeRepo).not.toBeNull());
    expect(blank.result.current.activeRepo?.syncedLabel).toBe("not synced");
  });
});
