/* PriorPrs — count badge, expandable rows with GitHub links; hidden when empty or on error. */
import { describe, it, expect, afterEach } from "vitest";
import type { PrHistory } from "@devdigest/shared";
import { renderWithProviders, screen, cleanup, within, waitFor, act } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { PriorPrs } from "./PriorPrs";
import { FILES_SHOWN } from "./constants";

const HISTORY: PrHistory = {
  history: [
    {
      pr_number: 410,
      title: "Newer limiter",
      merged_at: "2026-02-10T12:00:00Z",
      author: "bob",
      files_overlap: ["src/other.ts", "src/rl.ts"],
      notes: "",
    },
    { pr_number: 401, title: "Older limiter", merged_at: "2026-02-01T12:00:00Z", author: "ann", files_overlap: ["src/rl.ts"], notes: "" },
  ],
};

afterEach(cleanup);

describe("PriorPrs", () => {
  it("shows a count badge, then lists each PR with its link, author, date and overlapping files", async () => {
    mockFetch({ "GET /pulls/pr-1/history": HISTORY });
    const { user } = renderWithProviders(<PriorPrs prId="pr-1" repoFullName="acme/api" />);

    const toggle = await screen.findByRole("button", { name: /Prior PRs touching these files/ });
    expect(within(toggle).getByText("2")).toBeInTheDocument();
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("link", { name: "Newer limiter" })).toBeNull();

    await user.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    const link = screen.getByRole("link", { name: "Newer limiter" });
    expect(link).toHaveAttribute("href", "https://github.com/acme/api/pull/410");
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", expect.stringContaining("noopener"));
    expect(screen.getByText("#410")).toBeInTheDocument();
    expect(screen.getByText(/^bob · /)).toBeInTheDocument();
    const touched = screen.getByText("Touched 2 of these files: other.ts, rl.ts");
    expect(touched).toHaveAttribute("title", "src/other.ts\nsrc/rl.ts");
    expect(screen.getByText("Touched this file: rl.ts")).toBeInTheDocument();
  });

  it("shows a PR's notes as plain text instead of its files when it has some", async () => {
    mockFetch({
      "GET /pulls/pr-1/history": {
        history: [
          { pr_number: 7, title: "Has notes", merged_at: "2026-02-01T12:00:00Z", author: "ann", files_overlap: ["src/rl.ts"], notes: "Split out <b>the</b> router." },
          { pr_number: 8, title: "No notes", merged_at: "2026-02-02T12:00:00Z", author: "bob", files_overlap: ["src/a.ts"], notes: "  " },
        ],
      },
    });
    const { user } = renderWithProviders(<PriorPrs prId="pr-1" repoFullName="acme/api" />);
    await user.click(await screen.findByRole("button", { name: /Prior PRs touching these files/ }));

    expect(screen.getByText("Split out <b>the</b> router.")).toBeInTheDocument();
    expect(screen.getByText("Touched this file: a.ts")).toBeInTheDocument();
    expect(screen.queryByText(/rl\.ts/)).toBeNull();
  });

  it("names only the basenames of the first files in one prose line, keeping the full paths in a tooltip", async () => {
    const files = Array.from({ length: FILES_SHOWN + 6 }, (_, i) => `src/deep/dir/f${i}.ts`);
    mockFetch({
      "GET /pulls/pr-1/history": {
        history: [{ pr_number: 1, title: "Big refactor", merged_at: "2026-02-01T12:00:00Z", author: "ann", files_overlap: files, notes: "" }],
      },
    });
    const { user } = renderWithProviders(<PriorPrs prId="pr-1" repoFullName="acme/api" />);
    await user.click(await screen.findByRole("button", { name: /Prior PRs touching these files/ }));

    const names = files.slice(0, FILES_SHOWN).map((f) => f.split("/").pop());
    const line = screen.getByText(`Touched ${files.length} of these files: ${names.join(", ")}, … +6 more`);
    expect(line).toHaveAttribute("title", files.join("\n"));
    expect(line.textContent).not.toContain("src/deep");
    expect(screen.queryByRole("list", { name: /overlapping/ })).toBeNull();
  });

  it("renders nothing for an empty history", async () => {
    const api = mockFetch({ "GET /pulls/pr-1/history": { history: [] } });
    renderWithProviders(<PriorPrs prId="pr-1" repoFullName="acme/api" />);
    await waitFor(() => expect(api.requests("GET", "/pulls/pr-1/history")).toHaveLength(1));
    await act(async () => {});
    expect(screen.queryByRole("button")).toBeNull();
    expect(screen.queryByText(/Prior PRs touching these files/)).toBeNull();
  });

  it("renders nothing when the request fails", async () => {
    const api = mockFetch({ "GET /pulls/pr-1/history": jsonResponse({ error: { code: "boom", message: "x" } }, 500) });
    renderWithProviders(<PriorPrs prId="pr-1" repoFullName="acme/api" />);
    await waitFor(() => expect(api.requests("GET", "/pulls/pr-1/history")).toHaveLength(1));
    await act(async () => {});
    expect(screen.queryByRole("button")).toBeNull();
    expect(screen.queryByText(/Prior PRs touching these files/)).toBeNull();
  });
});
