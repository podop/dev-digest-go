import { describe, it, expect, afterEach, vi } from "vitest";
import { renderWithProviders, screen, cleanup } from "@/test/render";
import { mockFetch } from "@/test/fetch-mock";
import { GITHUB_REPO, GITLAB_REPO } from "@/test/code-host-repo";
import type { FindingRecord } from "@devdigest/shared";

vi.mock("next/navigation", () => ({ usePathname: () => "/repos/r1/pulls/12" }));

import { RepoProvider } from "@/lib/repo-context";
import { FindingCard } from "./FindingCard";

afterEach(cleanup);

const FINDING: FindingRecord = {
  id: "f1",
  severity: "CRITICAL",
  category: "security",
  title: "Hardcoded Stripe secret key",
  file: "src/config.ts",
  start_line: 11,
  end_line: 11,
  rationale: "A **live** Stripe key is committed in source.",
  suggestion: "Move the key to an environment variable.",
  confidence: 0.95,
  kind: "finding",
  trifecta_components: null,
  evidence: null,
  review_id: "r1",
  accepted_at: null,
  dismissed_at: null,
};

describe("FindingCard (smoke, both themes)", () => {
  (["dark", "light"] as const).forEach((theme) => {
    it(`renders severity + file:line + rationale in ${theme}`, () => {
      renderWithProviders(
        <div data-theme={theme}>
          <FindingCard f={FINDING} defaultExpanded onAction={() => {}} />
        </div>,
      );
      expect(screen.getByText("Hardcoded Stripe secret key")).toBeInTheDocument();
      expect(screen.getByText("src/config.ts:11")).toBeInTheDocument();
      // category label is shown alongside the severity badge
      expect(screen.getByText("security")).toBeInTheDocument();
    });
  });

  it("fires accept/dismiss actions", async () => {
    const onAction = vi.fn();
    const { user } = renderWithProviders(<FindingCard f={FINDING} defaultExpanded onAction={onAction} />);
    await user.click(screen.getByText("Accept"));
    expect(onAction).toHaveBeenCalledWith("accept");
    await user.click(screen.getByText("Dismiss"));
    expect(onAction).toHaveBeenCalledWith("dismiss");
  });
});

describe("FindingCard — accessible toggle", () => {
  it("the title is a button that expands and collapses the details", async () => {
    const { user } = renderWithProviders(<FindingCard f={FINDING} onAction={() => {}} />);
    const toggle = screen.getByRole("button", { name: "Hardcoded Stripe secret key" });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText("Accept")).toBeNull();
    toggle.focus();
    await user.keyboard("{Enter}");
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText("Accept")).toBeInTheDocument();
    await user.keyboard(" ");
    expect(toggle).toHaveAttribute("aria-expanded", "false");
  });

  it("clicking the header area still toggles (single toggle, not twice)", async () => {
    const { user } = renderWithProviders(<FindingCard f={FINDING} onAction={() => {}} />);
    await user.click(screen.getByText("security"));
    expect(screen.getByRole("button", { name: "Hardcoded Stripe secret key" })).toHaveAttribute("aria-expanded", "true");
    await user.click(screen.getByRole("button", { name: "Hardcoded Stripe secret key" }));
    expect(screen.getByRole("button", { name: "Hardcoded Stripe secret key" })).toHaveAttribute("aria-expanded", "false");
  });
});

describe("FindingCard — extraActions slot", () => {
  it("renders caller-supplied actions in the actions row of an expanded card", async () => {
    const { user } = renderWithProviders(
      <FindingCard f={FINDING} onAction={() => {}} extraActions={<button type="button">Extra</button>} />,
    );
    expect(screen.queryByRole("button", { name: "Extra" })).toBeNull();
    await user.click(screen.getByRole("button", { name: "Hardcoded Stripe secret key" }));
    expect(screen.getByRole("button", { name: "Extra" })).toBeInTheDocument();
  });
});

describe("FindingCard — file link on the repository's code host (FR41; AC32)", () => {
  const SPACED: FindingRecord = { ...FINDING, file: "src/a b.ts", start_line: 10, end_line: 20 };

  function renderOn(repo: typeof GITLAB_REPO, props: { repoFullName?: string | null } = {}) {
    mockFetch({ "GET /repos": [repo] });
    return renderWithProviders(
      <RepoProvider>
        <FindingCard f={SPACED} headSha="abc123" onAction={() => {}} {...props} />
      </RepoProvider>,
    );
  }

  it("links a GitLab finding to <web_url>/-/blob/<sha>/<path>#L10-20", async () => {
    renderOn(GITLAB_REPO);
    expect(await screen.findByRole("link", { name: "src/a b.ts:10-20" })).toHaveAttribute(
      "href",
      "https://git.corp.example/gitlab/acme/platform/payments/-/blob/abc123/src/a%20b.ts#L10-20",
    );
  });

  it("links a GitHub finding as before, with or without web_url", async () => {
    renderOn(GITHUB_REPO, { repoFullName: "acme/api" });
    expect(await screen.findByRole("link", { name: "src/a b.ts:10-20" })).toHaveAttribute(
      "href",
      "https://github.com/acme/api/blob/abc123/src/a%20b.ts#L10-L20",
    );
    cleanup();
    renderOn({ ...GITHUB_REPO, web_url: "https://github.com/acme/api" });
    expect(await screen.findByRole("link", { name: "src/a b.ts:10-20" })).toHaveAttribute(
      "href",
      "https://github.com/acme/api/blob/abc123/src/a%20b.ts#L10-L20",
    );
  });
});
