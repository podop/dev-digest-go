/* BlastRadiusCard — stats, grouped callers + links, empty and degraded states,
   Resync POST, through the real TanStack hooks with a stubbed fetch. */
import { describe, it, expect, afterEach } from "vitest";
import type { BlastRadius } from "@devdigest/shared";
import { renderWithProviders, screen, cleanup, within, waitFor } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { BlastRadiusCard } from "./BlastRadiusCard";

const BLAST: BlastRadius = {
  changed_symbols: [
    { name: "rateLimit", file: "src/rate-limit.ts", kind: "function" },
    { name: "audit", file: "src/audit.ts", kind: "function" },
    { name: "unused", file: "src/unused.ts", kind: "function" },
  ],
  downstream: [
    {
      symbol: "rateLimit",
      callers: [
        { name: "publicRouter", file: "src/router.ts", line: 23 },
        { name: "sweep", file: "src/cron.ts", line: 5 },
      ],
      endpoints_affected: ["GET /public", "POST /webhooks"],
      crons_affected: ["nightly-sweep"],
    },
    {
      symbol: "audit",
      callers: [{ name: "publicRouter", file: "src/router.ts", line: 23 }],
      endpoints_affected: ["GET /public"],
      crons_affected: [],
    },
  ],
  summary: "3 changed symbols · 2 callers · 2 endpoints · 1 cron",
  degraded: false,
};

function renderCard(headSha: string | null = "abc123") {
  return renderWithProviders(<BlastRadiusCard prId="pr-1" repoId="r1" repoFullName="acme/api" headSha={headSha} />);
}

afterEach(cleanup);

describe("BlastRadiusCard", () => {
  it("shows distinct stats, the grouped callers as GitHub links and the endpoint / cron chips", async () => {
    mockFetch({ "GET /pulls/pr-1/blast": BLAST });
    renderCard();

    const card = await screen.findByRole("region", { name: "Blast radius" });
    const stats = within(card).getAllByRole("listitem").slice(0, 4).map((li) => li.textContent);
    expect(stats).toEqual(["3symbols", "2callers", "2endpoints", "1cron"]);

    expect(within(card).getByRole("button", { name: /rateLimit\(\)/ })).toHaveAttribute("aria-expanded", "true");
    expect(within(card).getByRole("link", { name: "src/router.ts:23" })).toHaveAttribute(
      "href",
      "https://github.com/acme/api/blob/abc123/src/router.ts#L23",
    );
    expect(within(card).getByText("POST /webhooks")).toBeInTheDocument();
    expect(within(card).getByText("nightly-sweep")).toBeInTheDocument();
    expect(within(card).queryByText("Degraded")).toBeNull();
  });

  it("toggles between the tree and the graph", async () => {
    mockFetch({ "GET /pulls/pr-1/blast": BLAST });
    const { user } = renderCard();

    const tree = await screen.findByRole("button", { name: "Tree" });
    const graph = screen.getByRole("button", { name: "Graph" });
    expect(tree).toHaveAttribute("aria-pressed", "true");
    expect(graph).toHaveAttribute("aria-pressed", "false");
    expect(screen.getByRole("button", { name: /rateLimit\(\)/ })).toBeInTheDocument();

    await user.click(graph);
    expect(graph).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("img", { name: "Blast radius graph" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /rateLimit\(\)/ })).toBeNull();

    await user.click(tree);
    expect(screen.getByRole("button", { name: /rateLimit\(\)/ })).toBeInTheDocument();
    expect(screen.queryByRole("img", { name: "Blast radius graph" })).toBeNull();
  });

  it("shows the no-callers text with the symbol count instead of a blank card", async () => {
    mockFetch({
      "GET /pulls/pr-1/blast": { ...BLAST, downstream: [], summary: "s" } satisfies BlastRadius,
    });
    renderCard();
    expect(await screen.findByText("3 changed symbols, no downstream callers found.")).toBeInTheDocument();
  });

  it("shows the no-symbols text when the diff declares no symbols", async () => {
    mockFetch({ "GET /pulls/pr-1/blast": { changed_symbols: [], downstream: [], summary: "s" } satisfies BlastRadius });
    renderCard();
    expect(await screen.findByText("No symbols declared in the changed files.")).toBeInTheDocument();
  });

  it("without a head sha the callers are plain text", async () => {
    mockFetch({ "GET /pulls/pr-1/blast": BLAST });
    renderCard(null);
    expect(await screen.findByText("src/router.ts:23")).toBeInTheDocument();
    expect(screen.queryByRole("link")).toBeNull();
  });

  it("a degraded result shows the badge and reason, never the empty text, and Resync posts then refetches", async () => {
    const api = mockFetch({
      "GET /pulls/pr-1/blast": { changed_symbols: [], downstream: [], summary: "s", degraded: true, reason: "no_data" } satisfies BlastRadius,
      "POST /repos/r1/resync": jsonResponse({ status: "queued" }, 202),
    });
    const { user } = renderCard();

    const notice = within(await screen.findByRole("region", { name: "Blast radius" })).getByRole("status");
    expect(within(notice).getByText("Degraded")).toBeInTheDocument();
    expect(within(notice).getByText(/no index data for the files of this PR/)).toBeInTheDocument();
    expect(within(notice).getByText(/unknown, not “no impact”/)).toBeInTheDocument();
    expect(screen.queryByText("No symbols declared in the changed files.")).toBeNull();

    await user.click(within(notice).getByRole("button", { name: "Resync" }));
    await waitFor(() => expect(api.requests("POST", "/repos/r1/resync")).toHaveLength(1));
    await waitFor(() => expect(api.requests("GET", "/pulls/pr-1/blast")).toHaveLength(2));
    expect(await screen.findByText(/Resync started/)).toBeInTheDocument();
  });

  it("a degraded result with callers shows the reason badge together with the caller tree", async () => {
    mockFetch({ "GET /pulls/pr-1/blast": { ...BLAST, degraded: true, reason: "index_partial" } satisfies BlastRadius });
    renderCard();

    const card = await screen.findByRole("region", { name: "Blast radius" });
    const notice = within(card).getByRole("status");
    expect(within(notice).getByText("Degraded")).toBeInTheDocument();
    expect(within(notice).getByText(/index of this repo is partial/)).toBeInTheDocument();
    expect(within(card).getByRole("link", { name: "src/router.ts:23" })).toBeInTheDocument();
  });

  it("an API error shows the error state, and Retry reloads the card", async () => {
    const api = mockFetch({
      "GET /pulls/pr-1/blast": jsonResponse({ error: { code: "boom", message: "Index exploded" } }, 500),
    });
    const { user } = renderCard();

    expect(await screen.findByText("Couldn’t load the blast radius")).toBeInTheDocument();
    expect(screen.getByText("Index exploded")).toBeInTheDocument();
    api.on("GET /pulls/pr-1/blast", BLAST);
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByRole("region", { name: "Blast radius" })).toBeInTheDocument();
  });
});
