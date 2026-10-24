/* RepoRow — one repository line: name, branch, index state (A4) and the Remove button.
   Rendered alone; the index-state hook runs for real against a stubbed fetch. */
import { describe, it, expect, afterEach, vi } from "vitest";
import type { Repo } from "@/lib/types";
import { renderWithProviders, screen, cleanup, pseudoMessages } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { strayText } from "@/test/pseudo";
import { RepoRow } from "./RepoRow";

afterEach(cleanup);

const REPO: Repo = {
  id: "r1",
  workspace_id: "w1",
  owner: "acme",
  name: "api",
  full_name: "acme/api",
  default_branch: "main",
  clone_path: null,
  last_polled_at: null,
  created_by: null,
};

function indexState(sha: string, updatedAt: string) {
  return {
    repoId: "r1",
    status: "full",
    filesIndexed: 10,
    filesSkipped: 0,
    durationMs: 5,
    lastIndexedSha: sha,
    indexerVersion: 1,
    updatedAt,
  };
}

function renderRow(onRemove: () => void = () => {}) {
  return renderWithProviders(
    <ul>
      <RepoRow repo={REPO} onRemove={onRemove} />
    </ul>,
  );
}

describe("RepoRow", () => {
  it("shows name, branch and the indexed age, and Remove is named after the repo", async () => {
    mockFetch({ "GET /repos/r1/index-state": indexState("abc", new Date(Date.now() - 3 * 3_600_000).toISOString()) });
    const onRemove = vi.fn();
    const { user } = renderRow(onRemove);

    expect(screen.getByText("acme/api")).toBeInTheDocument();
    expect(screen.getByText("main")).toBeInTheDocument();
    expect(await screen.findByText("indexed 3 h ago")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Remove acme/api" }));
    expect(onRemove).toHaveBeenCalledTimes(1);
  });

  it("says 'not indexed yet' when there is no indexed commit, whatever the timestamp", async () => {
    mockFetch({ "GET /repos/r1/index-state": indexState("", new Date().toISOString()) });
    renderRow();
    expect(await screen.findByText("not indexed yet")).toBeInTheDocument();
    expect(screen.queryByText(/^indexed /)).not.toBeInTheDocument();
  });

  it("shows the unavailable text when the index state cannot be read, and Remove still works", async () => {
    mockFetch({
      "GET /repos/r1/index-state": jsonResponse({ error: { code: "internal", message: "boom" } }, 500),
    });
    const onRemove = vi.fn();
    const { user } = renderRow(onRemove);
    expect(await screen.findByText("index state unavailable")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Remove acme/api" }));
    expect(onRemove).toHaveBeenCalledTimes(1);
  });
});

describe("RepoRow messages (AC22)", () => {
  it.each([
    ["indexed", () => indexState("abc", new Date(Date.now() - 2 * 60_000).toISOString()), "⟦githubIntegration.repos.indexed.minutes⟧"],
    ["not indexed", () => indexState("", "1970-01-01T00:00:00Z"), "⟦githubIntegration.repos.notIndexed⟧"],
  ])("takes every visible string from the message files (%s)", async (_name, state, marker) => {
    mockFetch({ "GET /repos/r1/index-state": state() });
    const { container } = renderWithProviders(
      <ul>
        <RepoRow repo={REPO} onRemove={() => {}} />
      </ul>,
      { messages: pseudoMessages() },
    );
    expect(await screen.findByText(marker)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "⟦githubIntegration.repos.removeLabel⟧" })).toBeInTheDocument();
    expect(strayText(container, ["acme/api", "main"])).toEqual([]);
  });
});
