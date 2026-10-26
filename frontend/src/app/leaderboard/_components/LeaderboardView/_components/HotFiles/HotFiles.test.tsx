import { describe, expect, it } from "vitest";
import type { LeaderboardHotFile } from "@devdigest/shared";
import { renderWithProviders, screen, within, pseudoMessages } from "@/test/render";
import { strayText } from "@/test/pseudo";
import { repoLookup } from "../../helpers";
import { HotFiles } from "./HotFiles";

const files: LeaderboardHotFile[] = [
  { path: "internal/billing/invoice.go", repo_id: "id-acme/api", repo: "acme/api", findings: 5, critical: 2, prs: [41, 44] },
  { path: "web/src/header.tsx", repo_id: "id-acme/web", repo: "acme/web", findings: 1, critical: 0, prs: [7] },
];

describe("HotFiles", () => {
  it("shows path, repo and PR numbers, findings, and the critical count only where there is one", () => {
    renderWithProviders(<HotFiles files={files} />);

    const rows = within(screen.getByRole("list", { name: "Hot files" })).getAllByRole("listitem");
    expect(rows).toHaveLength(2);

    const hot = within(rows[0]!);
    expect(hot.getByText("internal/billing/invoice.go")).toBeInTheDocument();
    expect(hot.getByText(/^acme\/api ·/)).toHaveTextContent("acme/api · #41, #44");
    expect(hot.getByText("5 findings")).toBeInTheDocument();
    expect(hot.getByText("2 critical")).toBeInTheDocument();
    expect(rows[0]).toHaveAttribute("data-critical", "true");

    const calm = within(rows[1]!);
    expect(calm.getByText("1 finding")).toBeInTheDocument();
    expect(calm.queryByText(/critical/)).not.toBeInTheDocument();
    expect(rows[1]).not.toHaveAttribute("data-critical");
  });

  it("links every PR number to that PR's page in its repository (AC22)", () => {
    renderWithProviders(
      <HotFiles files={[{ path: "a.go", repo_id: "id with/slash", repo: "acme/api", findings: 1, critical: 0, prs: [41, 1044] }]} />,
    );

    const row = within(screen.getByRole("list", { name: "Hot files" })).getByRole("listitem");
    expect(within(row).getByRole("link", { name: "#41" })).toHaveAttribute("href", "/repos/id%20with%2Fslash/pulls/41");
    expect(within(row).getByRole("link", { name: "#1044" })).toHaveAttribute("href", "/repos/id%20with%2Fslash/pulls/1044");
    expect(within(row).getAllByRole("link")).toHaveLength(2);
  });

  it("shows !n and the host for a GitLab repository, with no hard-coded copy (AC33)", () => {
    const repos = repoLookup([{ id: "gl", full_name: "acme/platform/payments", provider: "gitlab", host: "gitlab.com" }]);
    const gitlab: LeaderboardHotFile[] = [{ path: "a.go", repo_id: "gl", repo: "payments", findings: 1, critical: 0, prs: [12] }];
    const { unmount } = renderWithProviders(<HotFiles files={gitlab} repos={repos} />);
    const row = within(screen.getByRole("list", { name: "Hot files" })).getByRole("listitem");
    expect(within(row).getByText(/^payments · gitlab\.com ·/)).toHaveTextContent("payments · gitlab.com · !12");
    expect(within(row).getByRole("link", { name: "!12" })).toHaveAttribute("href", "/repos/gl/pulls/12");
    unmount();

    const { container } = renderWithProviders(<HotFiles files={gitlab} repos={repos} />, { messages: pseudoMessages() });
    expect(strayText(container, ["a.go", "!12", /^\d+$/])).toEqual([]);
  });

  it("shows the empty state without files", () => {
    renderWithProviders(<HotFiles files={[]} />);

    expect(screen.getByText("No hot files")).toBeInTheDocument();
    expect(screen.queryByRole("list")).not.toBeInTheDocument();
  });
});
