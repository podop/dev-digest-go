/* RemoveRepoDialog — confirm before DELETE /repos/:id (FR19, EC17). Rendered alone with a
   spy for onClose; the delete hook runs for real against a stubbed fetch. */
import { describe, it, expect, afterEach, vi } from "vitest";
import type { Repo } from "@/lib/types";
import { renderWithProviders, screen, cleanup, waitFor, within, pseudoMessages } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { strayText } from "@/test/pseudo";
import { RemoveRepoDialog } from "./RemoveRepoDialog";

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

const GL_REPO: Repo = {
  ...REPO,
  id: "r9",
  owner: "group/sub",
  name: "proj",
  full_name: "group/sub/proj",
  provider: "gitlab",
  host: "gitlab.com",
  web_url: "https://gitlab.com/group/sub/proj",
  connection_id: null,
};

describe("RemoveRepoDialog", () => {
  it("names the repo, traps focus, and Cancel closes without sending anything", async () => {
    const api = mockFetch({});
    const onClose = vi.fn();
    const { user } = renderWithProviders(<RemoveRepoDialog repo={REPO} onClose={onClose} />);

    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText("Remove acme/api?")).toBeInTheDocument();
    expect(within(dialog).getByText("DevDigest stops tracking this repository. Nothing changes on GitHub.")).toBeInTheDocument();
    for (let i = 0; i < 4; i++) {
      await user.tab();
      expect(dialog.contains(document.activeElement)).toBe(true);
    }

    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(api.requests("DELETE")).toHaveLength(0);
  });

  it("sends one DELETE for a double click, disables both buttons while it runs, then closes", async () => {
    let release: () => void = () => {};
    const gate = new Promise<void>((res) => (release = res));
    const api = mockFetch({
      "DELETE /repos/:id": async (req) => {
        await gate;
        return { deleted: req.params.id };
      },
    });
    const onClose = vi.fn();
    const { user } = renderWithProviders(<RemoveRepoDialog repo={REPO} onClose={onClose} />);

    await user.dblClick(within(screen.getByRole("dialog")).getByRole("button", { name: "Remove" }));
    const busy = await screen.findByRole("button", { name: "Removing…" });
    expect(busy).toBeDisabled();
    expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled();
    expect(onClose).not.toHaveBeenCalled();

    release();
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
    expect(api.requests("DELETE")).toHaveLength(1);
    expect(api.requests("DELETE")[0]!.path).toBe("/repos/r1");
  });

  it("keeps the dialog open with the API message on failure, and a retry can succeed", async () => {
    let fail = true;
    const api = mockFetch({
      "DELETE /repos/r1": () =>
        fail ? jsonResponse({ error: { code: "internal", message: "Disk busy" } }, 500) : { deleted: "r1" },
    });
    const onClose = vi.fn();
    const { user } = renderWithProviders(<RemoveRepoDialog repo={REPO} onClose={onClose} />);

    await user.click(screen.getByRole("button", { name: "Remove" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Disk busy");
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(onClose).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Remove" })).toBeEnabled();

    fail = false;
    await user.click(screen.getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
    expect(api.requests("DELETE")).toHaveLength(2);
  });
});

describe("RemoveRepoDialog for a GitLab repository (FR37, AC29)", () => {
  it("names path and host, says nothing changes on GitLab, and deletes a not-connected repository by id", async () => {
    const api = mockFetch({ "DELETE /repos/:id": (req) => ({ deleted: req.params.id }) });
    const onClose = vi.fn();
    const { user } = renderWithProviders(<RemoveRepoDialog repo={GL_REPO} onClose={onClose} />);

    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText("Remove group/sub/proj · gitlab.com?")).toBeInTheDocument();
    expect(within(dialog).getByText("DevDigest stops tracking this repository. Nothing changes on GitLab.")).toBeInTheDocument();
    expect(within(dialog).queryByText(/GitHub/)).not.toBeInTheDocument();

    await user.click(within(dialog).getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
    expect(api.requests("DELETE")).toHaveLength(1);
    expect(api.requests("DELETE")[0]!.path).toBe("/repos/r9");
  });
});

describe("RemoveRepoDialog messages (AC22)", () => {
  it("takes every visible string from the message files (pseudo bundle), error state included", async () => {
    mockFetch({
      "DELETE /repos/r1": () => {
        throw new TypeError("network down");
      },
    });
    const { user } = renderWithProviders(<RemoveRepoDialog repo={REPO} onClose={() => {}} />, {
      messages: pseudoMessages(),
    });
    // The modal renders inside the dialog element; scan the whole document body.
    expect(screen.getByText("⟦githubIntegration.removeDialog.title⟧")).toBeInTheDocument();
    expect(screen.getByText(/⟦githubIntegration.removeDialog.body⟧/)).toBeInTheDocument();
    expect(strayText(document.body)).toEqual([]);

    await user.click(screen.getByRole("button", { name: "⟦githubIntegration.removeDialog.confirm⟧" }));
    // The message of an ApiError (here the client's own "cannot reach" text) is API data, not copy.
    const alert = await screen.findByRole("alert");
    expect(strayText(document.body, [alert.textContent?.trim() ?? ""])).toEqual([]);
  });

  it("takes the GitLab body and the path · host title from the message files too", () => {
    mockFetch({});
    renderWithProviders(<RemoveRepoDialog repo={GL_REPO} onClose={() => {}} />, { messages: pseudoMessages() });
    expect(screen.getByText("⟦githubIntegration.removeDialog.title⟧")).toBeInTheDocument();
    expect(screen.getByText(/⟦githubIntegration.removeDialog.bodyGitlab⟧/)).toBeInTheDocument();
    expect(strayText(document.body)).toEqual([]);
  });
});
