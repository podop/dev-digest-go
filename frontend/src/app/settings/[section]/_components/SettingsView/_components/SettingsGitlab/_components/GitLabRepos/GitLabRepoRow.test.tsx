/* GitLabRepoRow — index state line, incl. the "no supported source files" state (FR29, AC22). */
import { describe, it, expect, afterEach } from "vitest";
import type { Repo } from "@/lib/types";
import { renderWithProviders, screen, cleanup } from "@/test/render";
import { mockFetch } from "@/test/fetch-mock";
import { GITLAB_REPO } from "@/test/code-host-repo";
import { GitLabRepoRow } from "./GitLabRepoRow";

afterEach(cleanup);

const LANGUAGES = "Only TypeScript, JavaScript, Go and Java files are indexed.";

const indexState = (over: Record<string, unknown>) => ({
  repoId: "r1",
  status: "partial",
  filesIndexed: 0,
  filesSkipped: 0,
  durationMs: 5,
  lastIndexedSha: "abc",
  indexerVersion: 1,
  updatedAt: new Date(Date.now() - 3 * 3_600_000).toISOString(),
  ...over,
});

function renderRow() {
  return renderWithProviders(
    <ul>
      <GitLabRepoRow repo={GITLAB_REPO as Repo} onRemove={() => {}} />
    </ul>,
  );
}

describe("GitLabRepoRow index state", () => {
  it("says 'no supported source files' with the languages hint instead of 'indexed … ago'", async () => {
    mockFetch({ "GET /repos/r1/index-state": indexState({ reason: "no_files" }) });
    renderRow();
    expect(await screen.findByText("no supported source files")).toBeInTheDocument();
    expect(screen.getByText(LANGUAGES)).toBeInTheDocument();
    expect(screen.queryByText(/^indexed /)).not.toBeInTheDocument();
  });

  it("renders as before for a partial index that has files (AC22)", async () => {
    mockFetch({ "GET /repos/r1/index-state": indexState({ filesIndexed: 12, reason: "budget" }) });
    renderRow();
    expect(await screen.findByText("indexed 3 h ago")).toBeInTheDocument();
    expect(screen.queryByText("no supported source files")).not.toBeInTheDocument();
    expect(screen.queryByText(LANGUAGES)).not.toBeInTheDocument();
  });
});
