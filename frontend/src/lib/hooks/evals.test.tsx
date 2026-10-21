import { afterEach, describe, expect, it, vi } from "vitest";
import type { EvalRunDetail, EvalRunSummary } from "@devdigest/shared";
import { renderHookWithProviders, cleanup, act, waitFor } from "@/test/render";
import { mockFetch, jsonResponse } from "@/test/fetch-mock";
import { makeCase } from "@/test/eval-fixtures";
import { evalKeys, prKeys } from "./keys";
import {
  EVAL_RUN_POLL_MS,
  useCreateEvalCase,
  useCreateManualEvalCase,
  useDeleteEvalCase,
  useEvalAgentDashboard,
  useEvalRun,
  useEvalRuns,
  useSkillEvalCases,
  useSkillEvalRuns,
  useStartSkillEvalRun,
  useUpdateEvalCase,
} from "./evals";

afterEach(() => {
  vi.useRealTimers();
  cleanup();
});

function makeRun(overrides: Partial<EvalRunSummary> = {}): EvalRunSummary {
  return {
    id: "run1",
    owner_kind: "agent",
    skill_id: null,
    skill_version: null,
    agent_id: "ag1",
    agent_version: 3,
    agent_name: "Security Reviewer",
    model: "mock/reviewer",
    status: "running",
    started_at: "2026-10-14T10:00:00.000Z",
    finished_at: null,
    cases_total: 8,
    cases_finished: 0,
    cases_passed: 0,
    cases_errored: 0,
    recall: null,
    precision: null,
    citation_accuracy: null,
    unlabeled_findings: 0,
    cost_usd: null,
    duration_ms: null,
    error: null,
    ...overrides,
  };
}

const detail = (run: EvalRunSummary): EvalRunDetail => ({ run, results: [] });

describe("useEvalRun", () => {
  it("polls every 2 s while running, then stops and invalidates the agent's eval queries", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const responses = [
      detail(makeRun({ cases_finished: 2 })),
      detail(makeRun({ cases_finished: 5 })),
      detail(makeRun({ status: "completed", cases_finished: 8, cases_passed: 6, recall: 0.75, finished_at: "2026-10-14T10:01:00.000Z" })),
    ];
    let calls = 0;
    const api = mockFetch({ "GET /eval-runs/run1": () => responses[Math.min(calls++, responses.length - 1)] });
    const { result, queryClient } = renderHookWithProviders(() => useEvalRun("run1"));
    queryClient.setQueryData(evalKeys.runs("ag1", 20), { runs: [] });
    queryClient.setQueryData(evalKeys.overview(), { agents: [], recent_runs: [] });

    await waitFor(() => expect(result.current.data?.run.cases_finished).toBe(2));
    await act(() => vi.advanceTimersByTimeAsync(EVAL_RUN_POLL_MS));
    await waitFor(() => expect(result.current.data?.run.cases_finished).toBe(5));
    expect(queryClient.getQueryState(evalKeys.runs("ag1", 20))?.isInvalidated).toBe(false);

    await act(() => vi.advanceTimersByTimeAsync(EVAL_RUN_POLL_MS));
    await waitFor(() => expect(result.current.data?.run.status).toBe("completed"));
    await waitFor(() => expect(queryClient.getQueryState(evalKeys.runs("ag1", 20))?.isInvalidated).toBe(true));
    expect(queryClient.getQueryState(evalKeys.overview())?.isInvalidated).toBe(true);

    // Terminal status: no further polling.
    const fetched = api.requests("GET", "/eval-runs/run1").length;
    expect(fetched).toBe(3);
    await act(() => vi.advanceTimersByTimeAsync(EVAL_RUN_POLL_MS * 3));
    expect(api.requests("GET", "/eval-runs/run1")).toHaveLength(fetched);
  });
});

describe("useCreateEvalCase", () => {
  it("treats a 409 eval_case_exists as success and returns the existing case id", async () => {
    const api = mockFetch({
      "POST /findings/f1/eval-case": jsonResponse(
        { error: { code: "eval_case_exists", message: "exists", details: { case_id: "case9" } } },
        409,
      ),
    });
    const { result, queryClient } = renderHookWithProviders(() => useCreateEvalCase("pr1"));
    queryClient.setQueryData(prKeys.reviews("pr1"), []);

    act(() => result.current.mutate({ findingId: "f1", agentId: "ag1" }));
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toEqual({ created: false, caseId: "case9" });
    expect(api.requests("POST", "/findings/f1/eval-case")).toHaveLength(1);
    await waitFor(() => expect(queryClient.getQueryState(prKeys.reviews("pr1"))?.isInvalidated).toBe(true));
  });

  it("surfaces other errors (422 finding_undecided) on the mutation for inline display", async () => {
    mockFetch({
      "POST /findings/f1/eval-case": jsonResponse({ error: { code: "finding_undecided", message: "Accept or dismiss first" } }, 422),
    });
    const { result } = renderHookWithProviders(() => useCreateEvalCase("pr1"));

    act(() => result.current.mutate({ findingId: "f1", agentId: "ag1" }));
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(result.current.error).toMatchObject({ status: 422, code: "finding_undecided" });
  });
});

describe("period-aware reads", () => {
  it("sends period on the runs list and the dashboard, and nothing without it", async () => {
    const api = mockFetch({
      "GET /agents/ag1/eval-runs": { runs: [] },
      "GET /agents/ag1/eval/dashboard": { agent_id: "ag1", cases_total: 0, latest: null, previous: null, trend: [], alert: null },
    });
    const withPeriod = renderHookWithProviders(() => ({
      runs: useEvalRuns("ag1", 100, "7d"),
      dashboard: useEvalAgentDashboard("ag1", "7d"),
    }));
    await waitFor(() => expect(withPeriod.result.current.runs.isSuccess && withPeriod.result.current.dashboard.isSuccess).toBe(true));
    expect(api.requests("GET", "/agents/ag1/eval-runs")[0]?.search).toBe("?limit=100&period=7d");
    expect(api.requests("GET", "/agents/ag1/eval/dashboard")[0]?.search).toBe("?period=7d");
    expect(withPeriod.queryClient.getQueryState(evalKeys.runs("ag1", 100, "7d"))?.status).toBe("success");
    expect(withPeriod.queryClient.getQueryState(evalKeys.dashboard("ag1", "7d"))?.status).toBe("success");
    cleanup();

    const bare = renderHookWithProviders(() => ({ runs: useEvalRuns("ag1"), dashboard: useEvalAgentDashboard("ag1") }));
    await waitFor(() => expect(bare.result.current.runs.isSuccess && bare.result.current.dashboard.isSuccess).toBe(true));
    expect(api.requests("GET", "/agents/ag1/eval-runs")[1]?.search).toBe("?limit=20");
    expect(api.requests("GET", "/agents/ag1/eval/dashboard")[1]?.search).toBe("");
  });

  it("keys differ per period and stay under the agent prefix", () => {
    expect(evalKeys.runs("ag1", 100, "7d")).not.toEqual(evalKeys.runs("ag1", 100, "30d"));
    expect(evalKeys.dashboard("ag1", "7d")).not.toEqual(evalKeys.dashboard("ag1"));
    expect(evalKeys.dashboard("ag1", "7d").slice(0, evalKeys.agent("ag1").length)).toEqual(evalKeys.agent("ag1"));
    expect(evalKeys.runs("ag1")).toEqual([...evalKeys.agent("ag1"), "runs"]);
  });
});

describe("skill-owned eval reads and runs", () => {
  it("keys nest under the skill prefix, apart from the agent prefix", () => {
    expect(evalKeys.skillCases("sk1")).toEqual([...evalKeys.skill("sk1"), "cases"]);
    expect(evalKeys.skillRuns("sk1")).toEqual([...evalKeys.skill("sk1"), "runs"]);
    expect(evalKeys.skillRuns("sk1", 20)).toEqual([...evalKeys.skill("sk1"), "runs", 20]);
    expect(evalKeys.skill("sk1")).not.toEqual(evalKeys.agent("sk1"));
  });

  it("reads a skill's cases and runs and starts a run with the host agent", async () => {
    const started = makeRun({ id: "sr1", owner_kind: "skill", skill_id: "sk1", skill_version: 2, agent_id: "host1" });
    const api = mockFetch({
      "GET /skills/sk1/eval-cases": { cases: [makeCase({ owner_kind: "skill", owner_id: "sk1", agent_id: null })] },
      "GET /skills/sk1/eval-runs": { runs: [] },
      "POST /skills/sk1/eval-runs": started,
    });
    const { result, queryClient } = renderHookWithProviders(() => ({
      cases: useSkillEvalCases("sk1"),
      runs: useSkillEvalRuns("sk1"),
      start: useStartSkillEvalRun("sk1"),
    }));
    await waitFor(() => expect(result.current.cases.data).toHaveLength(1));
    await waitFor(() => expect(result.current.runs.isSuccess).toBe(true));
    expect(api.requests("GET", "/skills/sk1/eval-runs")[0]?.search).toBe("?limit=20");

    act(() => result.current.start.mutate({ agent_id: "host1" }));
    await waitFor(() => expect(result.current.start.isSuccess).toBe(true));
    expect(api.requests("POST", "/skills/sk1/eval-runs")[0]?.body).toEqual({ agent_id: "host1" });
    expect(queryClient.getQueryData<EvalRunDetail>(evalKeys.run("sr1"))?.run.id).toBe("sr1");
  });

  it("a finished skill run invalidates the skill's queries and leaves the agent's alone", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const skillRun = (over: Partial<EvalRunSummary>) => makeRun({ id: "sr1", owner_kind: "skill", skill_id: "sk1", agent_id: "host1", ...over });
    const responses = [detail(skillRun({})), detail(skillRun({ status: "completed", cases_finished: 8 }))];
    let calls = 0;
    mockFetch({ "GET /eval-runs/sr1": () => responses[Math.min(calls++, responses.length - 1)] });
    const { result, queryClient } = renderHookWithProviders(() => useEvalRun("sr1"));
    queryClient.setQueryData(evalKeys.skillRuns("sk1", 20), { runs: [] });
    queryClient.setQueryData(evalKeys.runs("host1", 20), { runs: [] });
    queryClient.setQueryData(evalKeys.overview(), { agents: [], recent_runs: [] });

    await waitFor(() => expect(result.current.data?.run.status).toBe("running"));
    await act(() => vi.advanceTimersByTimeAsync(EVAL_RUN_POLL_MS));
    await waitFor(() => expect(queryClient.getQueryState(evalKeys.skillRuns("sk1", 20))?.isInvalidated).toBe(true));
    expect(queryClient.getQueryState(evalKeys.runs("host1", 20))?.isInvalidated).toBe(false);
    expect(queryClient.getQueryState(evalKeys.overview())?.isInvalidated).toBe(false);
  });
});

describe("case mutations by owner", () => {
  it("creates a manual case on the owner's route and refreshes only that owner", async () => {
    const created = makeCase({ id: "c9", owner_kind: "skill", owner_id: "sk1", agent_id: null, origin: "manual" });
    const api = mockFetch({ "POST /skills/sk1/eval-cases": jsonResponse(created, 201) });
    const { result, queryClient } = renderHookWithProviders(() => useCreateManualEvalCase({ kind: "skill", id: "sk1" }));
    queryClient.setQueryData(evalKeys.skillCases("sk1"), { cases: [] });
    queryClient.setQueryData(evalKeys.cases("sk1"), { cases: [] });
    const input = {
      name: "n",
      expectation: { kind: "must_find" as const, file: "a.ts", start_line: 1, end_line: 1 },
      input_diff: "+x",
    };

    act(() => result.current.mutate(input));
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.requests("POST", "/skills/sk1/eval-cases")[0]?.body).toEqual(input);
    expect(queryClient.getQueryState(evalKeys.skillCases("sk1"))?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(evalKeys.cases("sk1"))?.isInvalidated).toBe(false);
  });

  it("surfaces a create error on the mutation (the editor shows it inline)", async () => {
    mockFetch({ "POST /agents/ag1/eval-cases": jsonResponse({ error: { code: "eval_case_limit", message: "limit" } }, 422) });
    const { result } = renderHookWithProviders(() => useCreateManualEvalCase({ kind: "agent", id: "ag1" }));
    act(() => result.current.mutate({ name: "n", expectation: { kind: "must_find", file: "a.ts", start_line: 1, end_line: 1 }, input_diff: "+x" }));
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(result.current.error).toMatchObject({ status: 422, code: "eval_case_limit" });
  });

  it("update and delete refresh the case's owner: a skill case leaves the agent keys alone", async () => {
    const skillCase = makeCase({ id: "c1", owner_kind: "skill", owner_id: "sk1", agent_id: null });
    mockFetch({ "PATCH /eval-cases/c1": skillCase, "DELETE /eval-cases/c1": jsonResponse(null, 204) });
    const { result, queryClient } = renderHookWithProviders(() => ({ update: useUpdateEvalCase(), remove: useDeleteEvalCase() }));
    const seed = () => {
      queryClient.setQueryData(evalKeys.skillCases("sk1"), { cases: [] });
      queryClient.setQueryData(evalKeys.cases("ag1"), { cases: [] });
    };
    seed();

    act(() => result.current.update.mutate({ id: "c1", patch: { name: "renamed" } }));
    await waitFor(() => expect(result.current.update.isSuccess).toBe(true));
    expect(queryClient.getQueryState(evalKeys.skillCases("sk1"))?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(evalKeys.cases("ag1"))?.isInvalidated).toBe(false);

    seed();
    act(() => result.current.remove.mutate({ id: "c1", owner: { kind: "skill", id: "sk1" } }));
    await waitFor(() => expect(result.current.remove.isSuccess).toBe(true));
    expect(queryClient.getQueryState(evalKeys.skillCases("sk1"))?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(evalKeys.cases("ag1"))?.isInvalidated).toBe(false);
  });
});
