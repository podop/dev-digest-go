import { readFileSync, readdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { z } from "zod";
import {
  ActiveRun,
  Agent,
  AgentColumn,
  AgentPeriodMetrics,
  AgentPerf,
  AgentPerfRow,
  AgentRunEstimate,
  AgentSkillLink,
  AgentStats,
  AgentVersion,
  AgentVersionConfig,
  BlastCaller,
  BlastRadius,
  BriefMissingInput,
  CategoryCount,
  ChangedSymbol,
  CommunitySkill,
  ConnTestResult,
  ContextAttachments,
  ContextDoc,
  ContextDocPreview,
  ContextList,
  Convention,
  ConventionScan,
  Conflict,
  ConflictTake,
  CostSegment,
  DownstreamImpact,
  FindingRecord,
  Intent,
  IntentSource,
  IntentTrace,
  ModelInfo,
  MultiAgentRun,
  MultiAgentRunSummary,
  ONBOARDING_LIMITS,
  OnboardingArchitecture,
  OnboardingEdge,
  OnboardingFirstTask,
  OnboardingNode,
  OnboardingPathItem,
  OnboardingRunStep,
  OnboardingTour,
  OnboardingTourNone,
  OnboardingTourReady,
  PerfSummary,
  PeriodInfo,
  PrBrief,
  PrBriefResponse,
  PrDetail,
  PrHistory,
  PrHistoryItem,
  PrIntentRecord,
  PrIntentResponse,
  PrMeta,
  PrReviewComment,
  ProjectContextTrace,
  ProjectContextTraceDoc,
  Repo,
  RepoIndexState,
  ReviewFocusItem,
  ReviewRecord,
  ReviewRunResponse,
  ReviewRunTarget,
  Risk,
  Risks,
  RunHistoryItem,
  RunLogLine,
  RunStats,
  RunSummary,
  RunTrace,
  PromptAssembly,
  SecretsStatus,
  SeverityBucket,
  Settings,
  Skill,
  SkillAgentRef,
  SkillImportPreview,
  SkillStats,
  SkillStatsSummary,
  SkillUsed,
  SkillVersion,
  SmartDiff,
  SmartDiffFile,
  SmartDiffGroup,
  StatPoint,
  ToolCall,
  UsageCount,
} from "@devdigest/shared";

// The Go backend's component tests record the response of every Phase-1 to Phase-4 route in
// backend/cmd/server/testdata/contracts. This file parses each recording with the vendored
// Zod contracts the UI uses, so a drift on either side fails here. The contracts themselves
// are never edited (frontend/AGENTS.md: src/vendor/shared is the wire contract).
const goldenDir = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "../../../backend/cmd/server/testdata/contracts",
);

const uuid = z.string().uuid();
const nullableString = z.string().nullable();
const ok = z.object({ ok: z.literal(true) }).strict();
const agentVersion = AgentVersion.extend({ config: AgentVersionConfig.strict() }).strict();
const conventionsState = z
  .object({ scan: ConventionScan.strict().nullable(), conventions: z.array(Convention.strict()) })
  .strict();
const contextPreview = ContextDocPreview.strict();
const attachments = ContextAttachments.strict();
// POST /repos/:id/resync answers one of two shapes (spec 7.2); neither may carry an extra key.
const resyncAccepted = z.object({ status: z.literal("accepted"), jobId: uuid }).strict();
const resyncDegraded = z
  .object({ status: z.literal("accepted"), degraded: z.literal(true), reason: z.literal("no_handler") })
  .strict();
const resync = z.union([resyncAccepted, resyncDegraded]);

// ---- Phase 4: reviews (spec 7.2-7.6). Every nested object is strict too: an extra key anywhere is
// a drift. ----
const findingRecord = FindingRecord.strict();
const reviewRecord = ReviewRecord.extend({ findings: z.array(findingRecord) }).strict();
const reviewRunResponse = ReviewRunResponse.extend({
  runs: z.array(ReviewRunTarget.strict()),
  reviews: z.array(reviewRecord),
}).strict();
const findingAnswer = z.object({ finding: findingRecord }).strict();
const learnAnswer = z.object({ finding: findingRecord, memoryId: uuid }).strict();
const runTrace = RunTrace.extend({
  config: RunTrace.shape.config.strict(),
  stats: RunStats.strict(),
  prompt_assembly: PromptAssembly.strict(),
  tool_calls: z.array(ToolCall.strict()),
  log: z.array(RunLogLine.strict()),
  skills_used: z.array(SkillUsed.strict()).nullish(),
  project_context: ProjectContextTrace.extend({ docs: z.array(ProjectContextTraceDoc.strict()) })
    .strict()
    .nullish(),
  intent: IntentTrace.extend({ sources: z.array(IntentSource.strict()).nullish() })
    .strict()
    .nullish(),
}).strict();
const multiAgentRun = MultiAgentRun.extend({
  columns: z.array(AgentColumn.extend({ findings: z.array(findingRecord) }).strict()),
  conflicts: z.array(Conflict.extend({ takes: z.array(ConflictTake.strict()) }).strict()),
}).strict();

// ---- Phase 5: intent, smart diff, blast, prior PRs, brief, onboarding, performance (spec 7.3). Every
// nested object is strict as well: a key the contract lacks is a drift. ----
const intentSource = IntentSource.strict();
const intentSnapshot = Intent.extend({ sources: z.array(intentSource).nullish() }).strict();
const prIntentResponse = PrIntentResponse.extend({
  intent: PrIntentRecord.extend({ sources: z.array(intentSource).nullish() }).strict().nullable(),
}).strict();
const smartDiff = SmartDiff.extend({
  groups: z.array(SmartDiffGroup.extend({ files: z.array(SmartDiffFile.strict()) }).strict()),
  split_suggestion: SmartDiff.shape.split_suggestion.strict(),
}).strict();
const blastRadius = BlastRadius.extend({
  changed_symbols: z.array(ChangedSymbol.strict()),
  downstream: z.array(DownstreamImpact.extend({ callers: z.array(BlastCaller.strict()) }).strict()),
}).strict();
const prHistory = PrHistory.extend({ history: z.array(PrHistoryItem.strict()) }).strict();
const prBrief = PrBrief.extend({
  risks: Risks.extend({ risks: z.array(Risk.strict()) }).strict(),
  review_focus: z.array(ReviewFocusItem.strict()).max(8),
  intent: intentSnapshot.nullable(),
  blast: blastRadius.nullable(),
  missing_inputs: z.array(BriefMissingInput.strict()),
}).strict();
const prBriefResponse = PrBriefResponse.extend({ brief: prBrief.nullable() }).strict();

const tourLimits = ONBOARDING_LIMITS;
const onboardingTour = OnboardingTour.extend({
  architecture: OnboardingArchitecture.extend({
    nodes: z.array(OnboardingNode.strict()).max(tourLimits.nodesMax),
    edges: z.array(OnboardingEdge.strict()).max(tourLimits.edgesMax),
  }).strict(),
  critical_paths: z.array(OnboardingPathItem.strict()).max(tourLimits.criticalPathsMax),
  run_steps: z.array(OnboardingRunStep.strict()).max(tourLimits.runStepsMax),
  reading_path: z.array(OnboardingPathItem.strict()).max(tourLimits.readingPathMax),
  first_tasks: z.array(OnboardingFirstTask.strict()).max(tourLimits.firstTasksMax),
}).strict();
const onboardingState = z.discriminatedUnion("status", [
  OnboardingTourNone.strict(),
  OnboardingTourReady.extend({ tour: onboardingTour }).strict(),
]);

const trend = z.array(StatPoint.strict());
const metrics = AgentPeriodMetrics.strict();
const costSegments = z.array(CostSegment.strict());
const perfSummary = PerfSummary.extend({
  most_active: z.object({ agent_id: z.string(), agent_name: z.string(), runs: z.number().int() }).strict().nullable(),
}).strict();
const agentPerf = AgentPerf.extend({
  period: PeriodInfo.strict(),
  summary: perfSummary,
  previous_summary: perfSummary,
  runs_trend: trend,
  agents: z.array(AgentPerfRow.extend({ metrics, previous: metrics, runs_trend: trend }).strict()),
  cost_by_agent: costSegments,
  cost_by_model: costSegments,
}).strict();
const agentStats = AgentStats.extend({
  period: PeriodInfo.strict(),
  metrics,
  previous: metrics,
  runs_trend: trend,
  cost_by_model: costSegments,
  skills_used: z.array(UsageCount.strict()),
  memory_pulled: z.array(UsageCount.strict()),
  findings_by_severity: z.array(SeverityBucket.strict()),
  findings_by_category: z.array(CategoryCount.strict()),
  run_history: z.array(
    RunHistoryItem.extend({ pr: z.object({ id: z.string(), repo_id: z.string(), number: z.number().int() }).strict().nullable() }).strict(),
  ),
}).strict();

// `.strict()` makes an extra key a failure: the Go API must not send fields the contract lacks.
const contracts: Record<string, z.ZodTypeAny> = {
  health: z.object({ status: z.literal("ok") }).strict(),
  health_ready: z.object({ ready: z.boolean() }).strict(),
  settings_get: Settings,
  settings_put: Settings,
  secrets_status: SecretsStatus.strict(),
  test_connection: ConnTestResult.strict(),
  repos_create: Repo.strict(),
  repos_list: z.array(Repo.strict()),
  workspace: z
    .object({
      workspaceId: uuid,
      cloneDir: z.string().min(1),
      repos: z.array(
        z
          .object({
            id: uuid,
            full_name: z.string(),
            clone_path: nullableString,
            last_polled_at: nullableString,
            cloned: z.boolean(),
          })
          .strict(),
      ),
    })
    .strict(),
  repo_refresh: z.object({ status: z.literal("refreshing") }).strict(),
  repo_delete: z.object({ deleted: uuid }).strict(),
  repo_poll: z.object({ synced: z.number().int(), reviewTriggered: z.boolean() }).strict(),
  // List items always carry an id and every rollup key (spec 7.4).
  pulls_list: z.array(PrMeta.strict()),
  acme_pulls_list: z.array(PrMeta.strict()),
  pull_detail: PrDetail.strict(),
  acme_pull_detail: PrDetail.strict(),
  pull_comments: z.array(PrReviewComment.strict()),
  pull_comment_create: PrReviewComment.strict(),

  // ---- Phase 2: agents ----
  agents_list: z.array(Agent.strict()),
  agent_get: Agent.strict(),
  agent_create: Agent.strict(),
  agent_update: Agent.strict(),
  agent_delete: ok,
  agent_versions: z.array(agentVersion),
  agent_version_get: agentVersion,
  agent_skills_list: z.array(AgentSkillLink.strict()),
  agent_skills_set: z.array(AgentSkillLink.strict()),
  agent_models: z.array(ModelInfo.strict()),
  providers_models: z.array(ModelInfo.strict()),

  // ---- Phase 2: skills ----
  skills_list: z.array(Skill.strict()),
  skills_stats: z.array(SkillStatsSummary.strict()),
  skills_community: z.array(CommunitySkill.strict()),
  skill_import_preview: SkillImportPreview.strict(),
  skill_get: Skill.strict(),
  skill_create: Skill.strict(),
  skill_update: Skill.strict(),
  skill_restore: Skill.strict(),
  skill_delete: ok,
  skill_versions: z.array(SkillVersion.strict()),
  skill_version_get: SkillVersion.strict(),
  skill_agents: z.array(SkillAgentRef.strict()),
  skill_stats: SkillStats.extend({ used_by: z.array(SkillAgentRef.strict()) }).strict(),

  // ---- Phase 2: conventions ----
  conventions_empty: conventionsState,
  conventions_get: conventionsState,
  conventions_extract: ConventionScan.strict(),
  convention_patch: Convention.strict(),
  conventions_skill: z
    .object({ skill: Skill.strict(), linked_agents: z.array(SkillAgentRef.strict()) })
    .strict(),

  // ---- Phase 2: project context ----
  context_list: ContextList.extend({ docs: z.array(ContextDoc.strict()) }).strict(),
  context_doc: contextPreview,
  context_file_create: contextPreview,
  context_file_save: contextPreview,
  context_file_rename: contextPreview,
  agent_context_get: attachments,
  agent_context_put: attachments,
  skill_context_get: attachments,
  skill_context_put: attachments,

  // ---- Phase 3: repo-intel ----
  repo_index_state_full: RepoIndexState.strict(),
  repo_index_state_degraded: RepoIndexState.strict(),
  repo_index_state_synth: RepoIndexState.strict(),
  repo_resync_accepted: resync,
  repo_resync_degraded: resync,

  // ---- Phase 4: reviews ----
  review_start_single: reviewRunResponse,
  review_start_multi: reviewRunResponse,
  review_active_runs: z.array(ActiveRun.strict()),
  review_runs: z.array(RunSummary.strict()),
  review_trace_completed: runTrace,
  review_trace_ended: runTrace,
  review_reviews: z.array(reviewRecord),
  review_ok: ok,
  review_accept: findingAnswer,
  review_dismiss: findingAnswer,
  review_learn: learnAnswer,
  review_multi_run: multiAgentRun,
  review_multi_list: z.array(MultiAgentRunSummary.strict()),
  review_estimates: z.array(AgentRunEstimate.strict()),

  // ---- Phase 5 ----
  intent_none: prIntentResponse,
  intent_refresh: prIntentResponse,
  review_trace_with_intent: runTrace,
  smart_diff: smartDiff,
  blast_full: blastRadius,
  blast_degraded: blastRadius,
  history: prHistory,
  history_empty: prHistory,
  brief_none: prBriefResponse,
  brief_generated: prBriefResponse,
  onboarding_none: onboardingState,
  onboarding_ready: onboardingState,
  onboarding_stale: onboardingState,
  agents_performance: agentPerf,
  agent_stats: agentStats,
};

const read = (name: string): unknown =>
  JSON.parse(readFileSync(path.join(goldenDir, `${name}.json`), "utf8"));

describe("Go backend golden responses", () => {
  it("has a contract for every recording and a recording for every contract", () => {
    const recorded = readdirSync(goldenDir)
      .filter((f) => f.endsWith(".json"))
      .map((f) => f.slice(0, -".json".length))
      .sort();
    expect(recorded).toEqual(Object.keys(contracts).sort());
  });

  it.each(Object.entries(contracts))("%s parses with its Zod contract", (name, schema) => {
    const result = schema.safeParse(read(name));
    expect(result.success, result.success ? "" : JSON.stringify(result.error.issues, null, 2)).toBe(true);
  });

  it("the Phase 2 scan golden is the mock scan: 3 kept rules, 1 dropped, cost 0.00042", () => {
    const state = read("conventions_get") as {
      scan: { kept: number; proposed: number; cost_usd: number; dropped: { reason: string }[] };
      conventions: unknown[];
    };
    expect(state.scan.kept).toBe(3);
    expect(state.scan.proposed).toBe(4);
    expect(state.scan.cost_usd).toBe(0.00042);
    expect(state.scan.dropped.map((d) => d.reason)).toEqual(["file_not_found"]);
    expect(state.conventions).toHaveLength(3);
  });

  it("settings carry the four seeded keys (Settings fills defaults, so check the raw body)", () => {
    expect(Object.keys(read("settings_get") as object).sort()).toEqual([
      "density",
      "polling_interval_min",
      "sync_to_folder",
      "theme",
    ]);
  });

  it("the index-state goldens cover a full, a degraded and a synthesised state", () => {
    const full = read("repo_index_state_full") as Record<string, unknown>;
    expect(full.status).toBe("full");
    for (const key of ["degraded", "degradedReason", "reason"]) expect(full).not.toHaveProperty(key);

    const degraded = read("repo_index_state_degraded") as Record<string, unknown>;
    expect(degraded).toMatchObject({ status: "degraded", reason: "no_clone", degraded: true, degradedReason: "no_data" });

    const synth = read("repo_index_state_synth") as Record<string, unknown>;
    expect(synth).toMatchObject({
      status: "degraded",
      filesIndexed: 0,
      filesSkipped: 0,
      durationMs: 0,
      reason: "no_data",
      lastIndexedSha: "",
      indexerVersion: 2,
      degraded: true,
      degradedReason: "no_data",
    });
  });

  it("the resync goldens are the accepted and the degraded answer, and the strict union rejects extra keys", () => {
    expect(resyncAccepted.safeParse(read("repo_resync_accepted")).success).toBe(true);
    expect(resyncDegraded.safeParse(read("repo_resync_degraded")).success).toBe(true);
    // A degraded answer with a jobId, or an accepted one with `degraded`, is a drift.
    const accepted = read("repo_resync_accepted") as object;
    expect(resync.safeParse({ ...accepted, degraded: true }).success).toBe(false);
    expect(resync.safeParse({ ...accepted, extra: 1 }).success).toBe(false);
    expect(resync.safeParse({ ...(read("repo_resync_degraded") as object), jobId: "x" }).success).toBe(false);
  });

  it.each(["pulls_list", "acme_pulls_list"])("%s items have an id and all rollup keys", (name) => {
    const items = read(name) as Record<string, unknown>[];
    expect(items.length).toBeGreaterThan(0);
    for (const item of items) {
      expect(z.string().uuid().safeParse(item.id).success).toBe(true);
      for (const key of [
        "score",
        "cost_usd",
        "latest_review_id",
        "last_reviewed_at",
        "latest_review_ids",
        "findings_counts",
      ]) {
        expect(item, key).toHaveProperty(key);
      }
    }
  });

  it("the review start answers carry an empty reviews list; one run for one agent, a parent for several", () => {
    const single = read("review_start_single") as { multi_agent_run_id: unknown; runs: unknown[]; reviews: unknown[] };
    expect(single.multi_agent_run_id).toBeNull();
    expect(single.runs).toHaveLength(1);
    expect(single.reviews).toEqual([]);

    const multi = read("review_start_multi") as { multi_agent_run_id: unknown; runs: unknown[]; reviews: unknown[] };
    expect(uuid.safeParse(multi.multi_agent_run_id).success).toBe(true);
    expect(multi.runs).toHaveLength(3);
    expect(multi.reviews).toEqual([]);
  });

  it("the review records have usage keys only through a run and never a grounding", () => {
    const reviews = read("review_reviews") as Record<string, unknown>[];
    expect(reviews.length).toBeGreaterThan(0);
    for (const review of reviews) {
      expect(review).not.toHaveProperty("grounding");
      expect(review.run_id).not.toBeNull();
      for (const key of ["cost_usd", "tokens_in", "tokens_out", "agent_name"]) expect(review, key).toHaveProperty(key);
      for (const finding of review.findings as Record<string, unknown>[]) {
        expect(finding.evidence).toBeNull();
        expect(finding).toHaveProperty("eval_case_id");
        expect(finding).not.toHaveProperty("memory_id");
      }
    }
  });

  it("accept and dismiss swap the two timestamps; Learn answers the memory id on the finding", () => {
    const accepted = (read("review_accept") as { finding: Record<string, unknown> }).finding;
    expect(accepted.accepted_at).toEqual(expect.any(String));
    expect(accepted.dismissed_at).toBeNull();
    expect(accepted).not.toHaveProperty("memory_id");

    const dismissed = (read("review_dismiss") as { finding: Record<string, unknown> }).finding;
    expect(dismissed.dismissed_at).toEqual(expect.any(String));
    expect(dismissed.accepted_at).toBeNull();

    const learned = read("review_learn") as { finding: { memory_id: unknown }; memoryId: string };
    expect(learned.finding.memory_id).toBe(learned.memoryId);
  });

  it("the trace goldens are a completed and an ended run; the live log has no 'Run complete' line", () => {
    const completed = read("review_trace_completed") as {
      stats: { findings: number; grounding: string };
      tool_calls: { tool: string; args: string; meta: string }[];
      log: { msg: string }[];
      intent: unknown;
      memory_pulled: unknown[];
    };
    expect(completed.stats).toMatchObject({ findings: 2, grounding: "2/2 passed" });
    expect(completed.tool_calls).toMatchObject([{ tool: "review_file", args: "all files", meta: "single-pass" }]);
    expect(completed.log.at(-1)?.msg).toMatch(/^Persisted review /);
    expect(completed.intent).toBeNull();
    expect(completed.memory_pulled).toEqual([]);

    const ended = read("review_trace_ended") as {
      stats: { findings: number; grounding: string };
      tool_calls: unknown[];
      raw_output: string;
      log: { kind: string; msg: string }[];
      prompt_assembly: Record<string, unknown>;
    };
    expect(ended.stats).toMatchObject({ findings: 0, grounding: "0/0 passed" });
    expect(ended.tool_calls).toEqual([]);
    expect(ended.raw_output).toBe("");
    expect(ended.log.at(-1)).toMatchObject({ kind: "error", msg: "Run cancelled by user" });
    // The ended assembly has no callers, repo_map, pr_description or intent key.
    expect(Object.keys(ended.prompt_assembly).sort()).toEqual(["memory", "skills", "specs", "system", "user"]);
  });

  it("the run history lists every run newest first, with the cancelled run's note and no blockers", () => {
    const runs = read("review_runs") as { status: string; error: string | null; blockers: number | null }[];
    expect(runs.map((r) => r.status)).toEqual(["done", "cancelled", "done"]);
    expect(runs[1]).toMatchObject({ error: "Cancelled by user", blockers: null });
  });

  it("the multi-agent golden has done columns only in the groups, and one conflict", () => {
    const run = read("review_multi_run") as {
      agent_count: number;
      columns: { status: string; agent_name: string }[];
      conflicts: { is_conflict: boolean; takes: unknown[] }[];
    };
    expect(run.agent_count).toBe(3);
    expect(run.columns.map((c) => c.agent_name)).toEqual([
      "Performance Reviewer",
      "Security Reviewer",
      "Test Quality Reviewer",
    ]);
    expect(run.columns.every((c) => c.status === "done")).toBe(true);
    expect(run.conflicts.map((c) => c.is_conflict)).toEqual([false, true]);
    for (const group of run.conflicts) expect(group.takes).toHaveLength(3);
  });

  it("the intent goldens: none, then a derived record with every source's detail key and the mock usage", () => {
    expect(read("intent_none")).toEqual({ intent: null, stale: false });
    const refreshed = read("intent_refresh") as { intent: Record<string, unknown>; stale: boolean };
    expect(refreshed.stale).toBe(false);
    expect(refreshed.intent).toMatchObject({
      change_type: "feature",
      prompt_version: 2,
      provider: "openrouter",
      tokens_in: 1200,
      tokens_out: 180,
      cost_usd: 0.00042,
    });
    for (const source of refreshed.intent.sources as Record<string, unknown>[]) expect(source).toHaveProperty("detail");
  });

  it("the review trace with an intent has a used intent, its assembly text and the scope event", () => {
    const trace = read("review_trace_with_intent") as {
      intent: { status: string; cache_hit: boolean; cost_usd: number };
      prompt_assembly: { intent: string };
      stats: { cost_usd: number };
      log: { msg: string }[];
    };
    expect(trace.intent).toMatchObject({ status: "used", cache_hit: false, cost_usd: 0.00042 });
    expect(trace.prompt_assembly.intent).toContain("Change type: feature");
    expect(trace.stats.cost_usd).toBe(0.00042); // the intent call is not added to the run's cost
    const msgs = trace.log.map((l) => l.msg);
    expect(msgs.findIndex((m) => m.startsWith("PR intent derived"))).toBeGreaterThan(0);
    expect(msgs.at(-2)).toMatch(/^scope: 0 out-of-scope finding\(s\) kept/);
  });

  it("the smart diff has the five groups in order and no pseudocode summary", () => {
    const diff = read("smart_diff") as { groups: { role: string; files: Record<string, unknown>[] }[] };
    expect(diff.groups.map((g) => g.role)).toEqual(["core", "tests", "wiring", "docs", "boilerplate"]);
    for (const file of diff.groups.flatMap((g) => g.files)) expect(file).not.toHaveProperty("pseudocode_summary");
  });

  it("a complete blast radius has degraded false and no reason; a degraded one has both", () => {
    const full = read("blast_full") as Record<string, unknown>;
    expect(full.degraded).toBe(false);
    expect(full).not.toHaveProperty("reason");
    expect(read("blast_degraded")).toMatchObject({ degraded: true, reason: "index_partial" });
  });

  it("the history goldens: one prior PR with its overlap, and the empty list", () => {
    const history = read("history") as { history: { notes: string; files_overlap: string[] }[] };
    expect(history.history).toHaveLength(1);
    const item = history.history[0];
    expect(item?.notes).toBe("");
    expect(item?.files_overlap).toEqual([...(item?.files_overlap ?? [])].sort());
    expect(read("history_empty")).toEqual({ history: [] });
  });

  it("the brief goldens: none, then a grounded brief with its snapshots and no history key", () => {
    expect(read("brief_none")).toEqual({ brief: null, stale: false });
    const { brief, stale } = read("brief_generated") as { brief: Record<string, unknown>; stale: boolean };
    expect(stale).toBe(false);
    expect(brief).toMatchObject({ prompt_version: "v1", model_requests: 1, tokens_in: 1200, cost_usd: 0.00042 });
    expect(brief).not.toHaveProperty("history");
    expect(brief.intent).not.toBeNull();
    expect(brief.blast).not.toBeNull();
  });

  it("the onboarding goldens: none, a fresh tour without stale_reason and a stale one", () => {
    expect(read("onboarding_none")).toEqual({ status: "none" });
    const ready = read("onboarding_ready") as Record<string, unknown>;
    expect(ready).toMatchObject({ status: "ready", stale: false });
    expect(ready).not.toHaveProperty("stale_reason");
    expect(read("onboarding_stale")).toMatchObject({ status: "ready", stale: true, stale_reason: "index_changed" });
  });

  it("the performance goldens: a Deleted agents row last, and the stats' metrics are the agent's dashboard row", () => {
    const perf = read("agents_performance") as {
      agents: { agent_id: string | null; agent_name: string; metrics: unknown }[];
    };
    expect(perf.agents.at(-1)).toMatchObject({ agent_id: null, agent_name: "Deleted agents" });
    const stats = read("agent_stats") as { agent_id: string; metrics: unknown };
    expect(stats.metrics).toEqual(perf.agents.find((a) => a.agent_id === stats.agent_id)?.metrics);
  });
});
