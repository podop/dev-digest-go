import { act } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Repo } from "@/lib/types";
import { renderWithProviders, screen, cleanup, waitFor } from "@/test/render";
import { mockFetch } from "@/test/fetch-mock";
import { IndexProgress } from "./IndexProgress";

const REPO = { id: "r1", full_name: "acme/api" } as Repo;
const SYNTH = { repoId: "r1", status: "degraded", lastIndexedSha: "", updatedAt: "1970-01-01T00:00:00Z", degraded: true };
const INDEXED = { repoId: "r1", status: "full", lastIndexedSha: "abc123", updatedAt: "2026-10-24T10:00:00Z" };

// Polling runs on the fake clock (frontend/INSIGHTS: fake timers + advanceTimersByTimeAsync).
beforeEach(() => vi.useFakeTimers({ shouldAdvanceTime: true }));
afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

const advance = (ms: number) => act(() => vi.advanceTimersByTimeAsync(ms));

describe("IndexProgress (FR25, EC16)", () => {
  it("refreshes every 3 s until the repo has an index state, then stops and shows its status", async () => {
    let calls = 0;
    const api = mockFetch({ "GET /repos/:id/index-state": () => (++calls < 3 ? SYNTH : INDEXED) });
    renderWithProviders(<IndexProgress repo={REPO} />);

    expect(await screen.findByText("Indexing acme/api…")).toBeInTheDocument();
    expect(api.requests("GET", "/repos/r1/index-state")).toHaveLength(1);
    await advance(2_500);
    expect(api.requests("GET", "/repos/r1/index-state")).toHaveLength(1);
    await advance(600);
    expect(api.requests("GET", "/repos/r1/index-state")).toHaveLength(2);
    await advance(3_000);
    expect(await screen.findByText("acme/api is indexed")).toBeInTheDocument();
    expect(screen.getByText("Full index")).toBeInTheDocument();

    await advance(30_000);
    expect(api.requests("GET", "/repos/r1/index-state")).toHaveLength(3);
    expect(screen.queryByText("Indexing continues in the background.")).toBeNull();
  });

  it("stops polling after 5 minutes without a state and says indexing continues in the background", async () => {
    const api = mockFetch({ "GET /repos/:id/index-state": SYNTH });
    renderWithProviders(<IndexProgress repo={REPO} />);
    await screen.findByText("Indexing acme/api…");
    expect(screen.queryByText("Indexing continues in the background.")).toBeNull();

    await advance(5 * 60_000 + 100);
    expect(await screen.findByText("Indexing continues in the background.")).toBeInTheDocument();
    const seen = api.requests("GET", "/repos/r1/index-state").length;
    expect(seen).toBeGreaterThan(50);
    await advance(30_000);
    await waitFor(() => expect(api.requests("GET", "/repos/r1/index-state")).toHaveLength(seen));
  });

  it("says the repo has no supported source files, with the languages line (FR30, AC22)", async () => {
    mockFetch({ "GET /repos/:id/index-state": { ...INDEXED, status: "partial", filesIndexed: 0, reason: "no_files" } });
    renderWithProviders(<IndexProgress repo={REPO} />);
    expect(await screen.findByText("acme/api has no supported source files")).toBeInTheDocument();
    expect(screen.getByText("Only TypeScript, JavaScript, Go and Java files are indexed.")).toBeInTheDocument();
    expect(screen.queryByText("acme/api is indexed")).toBeNull();
    expect(screen.queryByText("Partial index")).toBeNull();
  });

  it("renders a partial index with files as before (AC22)", async () => {
    mockFetch({ "GET /repos/:id/index-state": { ...INDEXED, status: "partial", filesIndexed: 12, reason: "budget" } });
    renderWithProviders(<IndexProgress repo={REPO} />);
    expect(await screen.findByText("acme/api is indexed")).toBeInTheDocument();
    expect(screen.getByText("Partial index")).toBeInTheDocument();
    expect(screen.queryByText(/no supported source files/)).toBeNull();
  });
});
