import { afterEach, describe, expect, it, vi } from "vitest";
import type { Repo } from "@/lib/types";
import { renderWithProviders, screen, cleanup, waitFor } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { RepoStep } from "./RepoStep";

afterEach(cleanup);

const repo = (id: string, full_name: string) => ({ id, full_name }) as Repo;
const finish = () => screen.getByRole("button", { name: "Finish" });
const INDEXED = { repoId: "r9", status: "partial", lastIndexedSha: "abc", updatedAt: "2026-10-24T10:00:00Z" };

describe("RepoStep", () => {
  it("adds a repository, shows its indexing status and Finish opens that repo (AC20)", async () => {
    const api = mockFetch({
      "POST /repos": repo("r9", "acme/api"),
      "GET /repos/:id/index-state": INDEXED,
    });
    const onFinish = vi.fn();
    const onBack = vi.fn();
    const { user } = renderWithProviders(<RepoStep repos={[]} onBack={onBack} onFinish={onFinish} />);
    expect(finish()).toBeDisabled();

    await user.type(screen.getByRole("textbox", { name: "Repository URL" }), "https://github.com/acme/api");
    await user.click(screen.getByRole("button", { name: "Add repository" }));
    expect(await screen.findByText("acme/api is indexed")).toBeInTheDocument();
    expect(screen.getByText("Partial index")).toBeInTheDocument();
    expect(api.requests("GET", "/repos/r9/index-state")).toHaveLength(1);
    // The form stays so another repository can be added.
    expect(screen.getByRole("heading", { name: "Add another repository" })).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "Repository URL" })).toBeInTheDocument();

    await waitFor(() => expect(finish()).toBeEnabled());
    await user.click(finish());
    expect(onFinish).toHaveBeenCalledWith("r9");
    await user.click(screen.getByRole("button", { name: "Back" }));
    expect(onBack).toHaveBeenCalledTimes(1);
  });

  it("keeps Finish disabled and shows the API message when the add fails (EC15)", async () => {
    mockFetch({ "POST /repos": jsonResponse({ error: { code: "conflict", message: "Repository already added" } }, 409) });
    const onFinish = vi.fn();
    const { user } = renderWithProviders(<RepoStep repos={[]} onFinish={onFinish} />);

    await user.type(screen.getByRole("textbox", { name: "Repository URL" }), "https://github.com/acme/api");
    await user.click(screen.getByRole("button", { name: "Add repository" }));
    expect(await screen.findByText("Repository already added")).toBeInTheDocument();
    expect(finish()).toBeDisabled();
    await user.click(finish());
    expect(onFinish).not.toHaveBeenCalled();
  });

  it("lists existing repositories above an 'Add another repository' form; adding one keeps Finish on it", async () => {
    const api = mockFetch({
      "POST /repos": repo("r9", "acme/new"),
      "GET /repos/:id/index-state": INDEXED,
    });
    const onFinish = vi.fn();
    const { user } = renderWithProviders(
      <RepoStep repos={[repo("r1", "acme/api"), repo("r2", "acme/web")]} onFinish={onFinish} />,
    );
    expect(screen.getByText("Your repositories")).toBeInTheDocument();
    expect(screen.getByText("acme/api")).toBeInTheDocument();
    expect(screen.getByText("acme/web")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Add another repository" })).toBeInTheDocument();
    // Finish is open for the first existing repo before anything is added.
    expect(finish()).toBeEnabled();

    await user.type(screen.getByRole("textbox", { name: "Repository URL" }), "https://github.com/acme/new");
    await user.click(screen.getByRole("button", { name: "Add repository" }));
    expect(await screen.findByText("acme/new is indexed")).toBeInTheDocument();
    expect(api.requests("POST", "/repos")).toHaveLength(1);
    // The list keeps the other repos and the form is ready for one more.
    expect(screen.getByText("acme/api")).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "Repository URL" })).toHaveValue("");

    await user.click(finish());
    expect(onFinish).toHaveBeenCalledWith("r9");
  });

  it("Finish opens the first existing repository when nothing is added", async () => {
    mockFetch({});
    const onFinish = vi.fn();
    const { user } = renderWithProviders(<RepoStep repos={[repo("r1", "acme/api")]} onFinish={onFinish} />);
    await user.click(finish());
    expect(onFinish).toHaveBeenCalledWith("r1");
  });
});
