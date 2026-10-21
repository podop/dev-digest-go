/* Agent Editor → Context tab: attached documents first in order with the
   "k of n attached" badge, the run-token footer (own + skill-inherited,
   de-duplicated, warning past the budget), one PUT per change, rollback + toast
   on a failed save, ordered saves for quick toggles and a repo switch — real
   hooks over a stubbed API. dnd-kit measures rows with getBoundingClientRect,
   which jsdom answers with zeros, so the rows get a stacked layout below. */
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { MutationCache, QueryClient } from "@tanstack/react-query";
import type { ContextList } from "@devdigest/shared";
import { renderWithProviders, screen, cleanup, waitFor, act } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { AGENT, makeSkill } from "@/test/skill-fixtures";
import { makeDoc, makeList, makeRepo } from "@/test/context-fixtures";
import { RepoProvider, useActiveRepo } from "@/lib/repo-context";
import { notify } from "@/lib/toast";
import { ContextTab } from "./ContextTab";

vi.mock("next/navigation", () => ({ usePathname: () => "/agents/ag1" }));

const ROW_HEIGHT = 40;

function stubRowLayout() {
  const original = Element.prototype.getBoundingClientRect;
  vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (this: Element) {
    const row = this.closest("[data-testid='context-doc-row']");
    if (!row) return original.call(this);
    const index = Array.from(document.querySelectorAll("[data-testid='context-doc-row']")).indexOf(row);
    const top = index * ROW_HEIGHT;
    return { x: 0, y: top, top, left: 0, width: 600, height: ROW_HEIGHT, right: 600, bottom: top + ROW_HEIGHT, toJSON: () => ({}) } as DOMRect;
  });
}

/** Server-side attachments, per "<owner>|<repo>". */
let store: Record<string, string[]>;
let lists: Record<string, ContextList>;
let api: ReturnType<typeof mockFetch>;
const puts = () => api.requests("PUT", "/agents/ag1/context").map((r) => (r.body as { paths: string[] }).paths);
const rowPaths = () => screen.getAllByTestId("context-doc-row").map((r) => r.dataset.path);

function RepoSwitch() {
  const { setRepoId } = useActiveRepo();
  return (
    <button type="button" onClick={() => setRepoId("r2")}>
      switch repo
    </button>
  );
}

function renderTab(queryClient?: QueryClient) {
  return renderWithProviders(
    <RepoProvider>
      <RepoSwitch />
      <ContextTab agent={AGENT} />
    </RepoProvider>,
    { queryClient },
  );
}

beforeEach(() => {
  localStorage.clear();
  stubRowLayout();
  store = {
    "agent|r1": ["specs/public-api.md", "specs/security-baseline.md"],
    "skill:s1|r1": ["docs/architecture.md", "specs/public-api.md"],
    "skill:s2|r1": ["insights/incident.md"],
  };
  lists = {
    r1: makeList(),
    r2: makeList({ docs: [makeDoc("docs/other-repo.md", { tokens: 50 })], tokens_total: 50 }),
  };
  api = mockFetch({
    "GET /repos": [makeRepo("r1", "payments-api"), makeRepo("r2", "web")],
    "GET /repos/:id/context": (req) => lists[req.params.id!],
    "GET /skills": [makeSkill({ id: "s1", name: "api-rules" }), makeSkill({ id: "s2", name: "off", enabled: false })],
    "GET /agents/ag1/skills": [
      { agent_id: "ag1", skill_id: "s1", order: 0 },
      { agent_id: "ag1", skill_id: "s2", order: 1 },
    ],
    "GET /skills/:id/context": (req) => {
      const repo = new URLSearchParams(req.search).get("repoId") ?? "";
      return { repo_id: repo, paths: store[`skill:${req.params.id}|${repo}`] ?? [] };
    },
    "GET /agents/ag1/context": (req) => {
      const repo = new URLSearchParams(req.search).get("repoId") ?? "";
      return { repo_id: repo, paths: store[`agent|${repo}`] ?? [] };
    },
    "PUT /agents/ag1/context": (req) => {
      const body = req.body as { repo_id: string; paths: string[] };
      store[`agent|${body.repo_id}`] = body.paths;
      return body;
    },
  });
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("Agent ContextTab", () => {
  it("shows 'k of n attached', attached rows first in order, and the run footer with skill-inherited tokens de-duplicated (AC19)", async () => {
    renderTab();
    expect(await screen.findByText("2 of 7 attached")).toBeInTheDocument();
    expect(rowPaths().slice(0, 3)).toEqual(["specs/public-api.md", "specs/security-baseline.md", "specs/rate-limiting.md"]);
    // own: public-api 200 + security-baseline 100; from the enabled skill: architecture 400
    // (public-api is already counted); the disabled skill's incident.md adds nothing.
    expect(await screen.findByText("≈ 700 tokens total")).toBeInTheDocument();
    expect(screen.getByText("incl. 400 from skills")).toBeInTheDocument();
    expect(screen.getByText(/Injected as an untrusted block/)).toBeInTheDocument();
    expect(screen.queryByText(/later documents will be skipped/)).not.toBeInTheDocument();
  });

  it("a toggle sends one PUT with the full ordered list at once; a keyboard move sends another (AC20)", async () => {
    const { user } = renderTab();
    await screen.findByText("2 of 7 attached");
    await user.click(screen.getByRole("checkbox", { name: "deployment.md" }));
    await waitFor(() => expect(puts()).toEqual([["specs/public-api.md", "specs/security-baseline.md", "docs/deployment.md"]]));
    expect(await screen.findByText("3 of 7 attached")).toBeInTheDocument();

    const handle = screen.getByRole("button", { name: "Drag to reorder docs/deployment.md" });
    act(() => handle.focus());
    await user.keyboard(" ");
    await user.keyboard("{ArrowUp}");
    await user.keyboard("{ArrowUp}");
    await user.keyboard(" ");
    await waitFor(() => expect(puts()).toHaveLength(2));
    expect(puts()[1]).toEqual(["docs/deployment.md", "specs/public-api.md", "specs/security-baseline.md"]);
  });

  it("restores the previous list and toasts when a save fails (AC20)", async () => {
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false, gcTime: Infinity }, mutations: { retry: false } },
      mutationCache: new MutationCache({ onError: (e) => notify.error(e.message) }),
    });
    api.on("PUT /agents/ag1/context", jsonResponse({ error: { code: "boom", message: "Save exploded" } }, 500));
    const { user } = renderTab(queryClient);
    await screen.findByText("2 of 7 attached");
    await user.click(screen.getByRole("checkbox", { name: "deployment.md" }));
    expect(await screen.findByText("Save exploded")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByText("2 of 7 attached")).toBeInTheDocument());
    expect(screen.getByRole("checkbox", { name: "deployment.md" })).toHaveAttribute("aria-checked", "false");
  });

  it("two quick toggles are saved in order and the server list ends equal to the screen (AC21)", async () => {
    let first = true;
    api.on("PUT /agents/ag1/context", async (req) => {
      const body = req.body as { repo_id: string; paths: string[] };
      if (first) {
        first = false;
        await new Promise((r) => setTimeout(r, 60));
      }
      store[`agent|${body.repo_id}`] = body.paths;
      return body;
    });
    const { user } = renderTab();
    await screen.findByText("2 of 7 attached");
    await user.click(screen.getByRole("checkbox", { name: "deployment.md" }));
    await user.click(screen.getByRole("checkbox", { name: "rate-limiting.md" }));
    await waitFor(() => expect(puts()).toHaveLength(2));
    expect(puts()[1]).toEqual(["specs/public-api.md", "specs/security-baseline.md", "docs/deployment.md", "specs/rate-limiting.md"]);
    await waitFor(() => expect(screen.getByText("4 of 7 attached")).toBeInTheDocument());
    await waitFor(() => expect(rowPaths().slice(0, 4)).toEqual(store["agent|r1"]));
    expect(store["agent|r1"]).toEqual(puts()[1]);
  });

  it("switching the active repo shows that repo's documents and attachments (AC25)", async () => {
    const { user } = renderTab();
    await screen.findByText("2 of 7 attached");
    await user.click(screen.getByRole("button", { name: "switch repo" }));
    expect(await screen.findByText("0 of 1 attached")).toBeInTheDocument();
    expect(rowPaths()).toEqual(["docs/other-repo.md"]);
    expect(screen.getByText("≈ 0 tokens total")).toBeInTheDocument();
  });

  it("warns when the estimate exceeds the 16k run budget", async () => {
    store["agent|r1"] = ["specs/public-api.md"];
    lists.r1 = makeList({ docs: [makeDoc("specs/public-api.md", { tokens: 17_000 })] });
    renderTab();
    expect(await screen.findByText("≈ 17,000 tokens total")).toBeInTheDocument();
    expect(screen.getByText(/later documents will be skipped/)).toBeInTheDocument();
  });

  it("without any repository, shows a hint instead of the list", async () => {
    api.on("GET /repos", []);
    renderTab();
    expect(await screen.findByText("No repository selected")).toBeInTheDocument();
    expect(api.requests("GET", "/agents/ag1/context")).toHaveLength(0);
  });
});
