/* useLinkRepo — FR41: links are built from the repository's own provider and web_url, so while the repository
   list is still loading (and without a RepoProvider) there is no repository and no link, never a guessed GitHub one. */
import { describe, it, expect, afterEach, vi } from "vitest";
import { renderWithProviders, screen, cleanup } from "@/test/render";
import { mockFetch } from "@/test/fetch-mock";
import { GITLAB_REPO } from "@/test/code-host-repo";

vi.mock("next/navigation", () => ({ usePathname: () => "/repos/r1/pulls/12" }));

import { RepoProvider } from "@/lib/repo-context";
import { useLinkRepo } from "./link-repo";

afterEach(cleanup);

function Probe() {
  const repo = useLinkRepo();
  return <p>{repo ? `${repo.provider ?? "github"} ${repo.full_name}` : "no repository"}</p>;
}

describe("useLinkRepo", () => {
  it("is null until the repository list has loaded, then the active repository (GitLab keeps its own host)", async () => {
    let release: () => void = () => {};
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    mockFetch({
      "GET /repos": async () => {
        await gate;
        return [GITLAB_REPO];
      },
    });

    renderWithProviders(
      <RepoProvider>
        <Probe />
      </RepoProvider>,
    );
    expect(screen.getByText("no repository")).toBeInTheDocument();

    release();
    expect(await screen.findByText("gitlab acme/platform/payments")).toBeInTheDocument();
  });

  it("is null without a RepoProvider", () => {
    renderWithProviders(<Probe />);
    expect(screen.getByText("no repository")).toBeInTheDocument();
  });
});
