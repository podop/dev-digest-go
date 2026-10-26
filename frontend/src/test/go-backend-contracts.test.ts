import { readFileSync, readdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { z } from "zod";
import {
  About,
  ActiveRun,
  Agent,
  AgentColumn,
  AgentEvalCase,
  AgentPeriodMetrics,
  AgentPerf,
  AgentPerfRow,
  AgentRunEstimate,
  AgentSkillLink,
  AgentStats,
  AgentVersion,
  AgentVersionConfig,
  ApiErrorBody,
  AutomaticReviewsStatus,
  BlastCaller,
  BlastRadius,
  BriefMissingInput,
  CategoryCount,
  ChangedSymbol,
  CiExport,
  CiFile,
  CiInstallation,
  CiRepoInstallations,
  CiRefreshResult,
  CiRun,
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
  CustomProvider,
  CustomProviderDeleted,
  CustomProviderHeader,
  CustomProviderInUseDetails,
  CustomProviderModel,
  DownstreamImpact,
  EvalAgentDashboard,
  EvalAgentOverview,
  EvalCaseInputMeta,
  EvalCaseLastResult,
  EvalCaseResult,
  EvalCompare,
  EvalExpectation,
  EvalOverview,
  EvalPromptDiffLine,
  EvalRecentRun,
  EvalRegressionAlert,
  EvalResultFinding,
  EvalRunAllResponse,
  EvalRunDetail,
  EvalRunSummary,
  EvalSparkPoint,
  FindingRecord,
  GitLabConnTestResult,
  GitLabConnection,
  GITLAB_CONNECTION_LIMITS,
  GITLAB_TEST_WARNING_CODES,
  GitLabConnectionDeleted,
  GitLabTestWarning,
  Intent,
  IntentSource,
  IntentTrace,
  Leaderboard,
  LeaderboardActivity,
  LeaderboardActivityPoint,
  LeaderboardAttention,
  LeaderboardAttentionReason,
  LeaderboardAward,
  LeaderboardAwards,
  LeaderboardHotFile,
  LeaderboardKpis,
  LeaderboardNeedsAttention,
  LeaderboardPeriod,
  LeaderboardRepo,
  LeaderboardRow,
  LeaderboardRowReview,
  LeaderboardSeverityCounts,
  LeaderboardSizeBucket,
  LeaderboardTopFinding,
  ModelInfo,
  ModelPricing,
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
  PLUGIN_DISABLED_CODE,
  PerfSummary,
  PeriodInfo,
  PluginAgent,
  PluginBundle,
  PluginContextFile,
  PluginConvention,
  PluginCounts,
  PluginEvalCase,
  PluginImportPreview,
  PluginImportResult,
  PluginLastExport,
  PluginLearning,
  PluginList,
  PluginManifest,
  PluginPreviewCounts,
  PluginRef,
  PluginRemoveResult,
  PluginSkill,
  PluginSkillVersion,
  InstalledPlugin,
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
  SPEND_CAP_ERROR_CODE,
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
  SpendCapDetails,
  SpendStatus,
  StatPoint,
  ToolCall,
  UsageCount,
  WORKSPACE_RESET_ACTIVE_CODE,
  WorkspaceInfo,
  WorkspaceRepo,
  WorkspaceResetResult,
  WorkspaceStats,
} from "@devdigest/shared";

// The Go backend's component tests record the response of every Phase-1 to Phase-6 route in
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

// ---- Phase 6: evals and export-to-CI (specs/eval-pipeline.md, specs/export-to-ci.md). Every nested
// object is strict as well. EvalExpectation is a refined object, so it is rebuilt strict from its
// inner object with the same end >= start rule. ----
const evalExpectation = EvalExpectation.innerType()
  .strict()
  .refine((e) => e.end_line >= e.start_line, { message: "end_line must be >= start_line", path: ["end_line"] });
const evalRunSummary = EvalRunSummary.strict();
const agentEvalCase = AgentEvalCase.extend({
  expectation: evalExpectation,
  input_meta: EvalCaseInputMeta.strict(),
  last_result: EvalCaseLastResult.strict().nullable(),
}).strict();
const evalRunDetail = EvalRunDetail.extend({
  run: evalRunSummary,
  results: z.array(
    EvalCaseResult.extend({
      expectation: evalExpectation,
      findings: z.array(EvalResultFinding.strict()),
    }).strict(),
  ),
}).strict();
const evalRunList = z.object({ runs: z.array(evalRunSummary) }).strict();
const evalCompareMetric = z.number().nullable();
const evalCompare = EvalCompare.extend({
  a: evalRunSummary,
  b: evalRunSummary,
  delta: z
    .object({
      recall: evalCompareMetric,
      precision: evalCompareMetric,
      citation_accuracy: evalCompareMetric,
      cost_usd: evalCompareMetric,
    })
    .strict(),
  cases: z.array(EvalCompare.shape.cases.element.strict()),
  config_diff: EvalCompare.shape.config_diff
    .extend({
      system_prompt: z.array(EvalPromptDiffLine.strict()),
      model: z.object({ a: z.string(), b: z.string() }).strict().nullable(),
      strategy: z.object({ a: z.string(), b: z.string() }).strict().nullable(),
    })
    .strict(),
}).strict();
const evalOverview = EvalOverview.extend({
  agents: z.array(
    EvalAgentOverview.extend({
      latest: evalRunSummary.nullable(),
      sparkline: z.array(EvalSparkPoint.strict()).max(10),
    }).strict(),
  ),
  recent_runs: z.array(EvalRecentRun.extend({ agent_name: z.string() }).strict()).max(20),
}).strict();
const evalAgentDashboard = EvalAgentDashboard.extend({
  latest: evalRunSummary.nullable(),
  previous: evalRunSummary.nullable(),
  trend: z.array(evalRunSummary),
  alert: EvalRegressionAlert.extend({
    metrics: z.array(EvalRegressionAlert.shape.metrics.element.strict()),
  })
    .strict()
    .nullable(),
}).strict();
const evalRunAll = EvalRunAllResponse.extend({
  started: z.array(evalRunSummary),
  skipped: z.array(EvalRunAllResponse.shape.skipped.element.strict()),
}).strict();

const ciRun = CiRun.strict();
const ciInstallation = CiInstallation.extend({ last_run: ciRun.nullish() }).strict();
const ciExport = CiExport.extend({
  installation: ciInstallation.nullable(),
  files: z.array(CiFile.strict()),
}).strict();
const ciRuns = z.object({ runs: z.array(ciRun) }).strict();
const ciRefresh = CiRefreshResult.extend({
  installations: z.array(
    CiRefreshResult.shape.installations.element
      .extend({ skipped: z.array(CiRefreshResult.shape.installations.element.shape.skipped.element.strict()) })
      .strict(),
  ),
}).strict();

// ---- PR Leaderboard (specs/2026-10-23-pr-leaderboard.md, GET /leaderboard). Every nested object is
// strict: an extra key anywhere is a drift. ----
const severityCounts = LeaderboardSeverityCounts.strict();
const leaderboardKpis = LeaderboardKpis.strict();
const leaderboardAward = LeaderboardAward.strict().nullable();
const leaderboard = Leaderboard.extend({
  period: LeaderboardPeriod.strict(),
  repos: z.array(LeaderboardRepo.strict()),
  summary: leaderboardKpis,
  previous_summary: leaderboardKpis.nullable(),
  awards: LeaderboardAwards.extend({
    cleanest: leaderboardAward,
    hottest: leaderboardAward,
    fastest_merge: leaderboardAward,
    most_rounds: leaderboardAward,
    monster: leaderboardAward,
    priciest: leaderboardAward,
  }).strict(),
  rows: z.array(
    LeaderboardRow.extend({
      findings: severityCounts,
      reviews: z.array(LeaderboardRowReview.extend({ findings: severityCounts }).strict()),
      top_findings: z.array(LeaderboardTopFinding.strict()).max(3),
    }).strict(),
  ),
  needs_attention: LeaderboardNeedsAttention.extend({
    items: z.array(LeaderboardAttention.extend({ reasons: z.array(LeaderboardAttentionReason.strict()) }).strict()),
  }).strict(),
  hot_files: z.array(LeaderboardHotFile.strict()),
  health_by_size: z.array(LeaderboardSizeBucket.strict()),
  activity: LeaderboardActivity.extend({ points: z.array(LeaderboardActivityPoint.strict()) }).strict(),
}).strict();

// ---- Settings slice A (specs/2026-10-23-settings-first-run-setup.md): GET /about, GET /settings/spend,
// the LLM test-connection answer and the 402 spend_cap_reached envelope. ----
const about = About.extend({
  runner: About.shape.runner.strict(),
  config_format: z.literal("devdigest-plugin/v1"),
}).strict();
// The platform contract keeps unpriced_count optional for older servers; the Go API always sends it.
const spendStatus = SpendStatus.extend({ unpriced_count: z.number().int().nonnegative() }).strict();
// The golden is an OpenRouter success: latency_ms, model and model_reachable are present; login and
// scopes belong to GitHub only (checked in the settings-slice-A test below).
const testConnectionLlm = ConnTestResult.extend({
  latency_ms: z.number().int().nonnegative(),
  model: z.string().min(1),
  model_reachable: z.boolean(),
}).strict();
const spendCapReached = ApiErrorBody.extend({
  error: ApiErrorBody.shape.error
    .extend({ code: z.literal(SPEND_CAP_ERROR_CODE), details: SpendCapDetails.strict() })
    .strict(),
}).strict();

// ---- Custom model providers (specs/2026-10-24-custom-model-providers.md §7): the settings routes. ----
// Secret values are never part of an answer: key_configured / value_configured are the only traces.
const customProvider = CustomProvider.extend({
  extra_headers: z.array(CustomProviderHeader.strict()),
  models: z.array(CustomProviderModel.extend({ pricing: ModelPricing.strict().nullable() }).strict()),
}).strict();
// A conflict that names what uses the provider; the details object is strict.
const customProviderInUse = ApiErrorBody.extend({
  error: ApiErrorBody.shape.error
    .extend({
      code: z.literal("provider_in_use"),
      details: CustomProviderInUseDetails.extend({ agents: z.array(CustomProviderInUseDetails.shape.agents.element.strict()) }).strict(),
    })
    .strict(),
}).strict();
// The connection test of a custom provider: warning is present (null when the list was read).
const customProviderTest = ConnTestResult.extend({
  provider: z.string().regex(/^custom:[a-z0-9-]{1,32}$/),
  latency_ms: z.number().int().nonnegative(),
  model: z.string().min(1),
  model_reachable: z.boolean().nullable(),
}).strict();

// ---- GitLab connections (specs/2026-10-24-gitlab-a-connection-sync.md §7, NFR7): the settings routes and
// the repos DTO. Every key is checked; the token is write-only, so no golden may carry one. ----
const gitlabConnection = GitLabConnection.extend({ id: uuid }).strict();
const gitlabConnectionDeleted = GitLabConnectionDeleted.extend({ deleted: uuid }).strict();
// The GitLab test answers with every key present (null when not known); unknown keys are a drift.
const gitlabConnTest = GitLabConnTestResult.extend({ warnings: z.array(GitLabTestWarning.strict()) }).strict();
const gitlabConnError = (code: string) =>
  ApiErrorBody.extend({ error: ApiErrorBody.shape.error.extend({ code: z.literal(code), details: z.undefined() }).strict() }).strict();
// 422 validation_error: details are the httpx issues (params.issue holds the Zod-style issue).
const gitlabConnInvalid = ApiErrorBody.extend({
  error: ApiErrorBody.shape.error
    .extend({
      code: z.literal("validation_error"),
      details: z.array(
        z
          .object({
            keyword: z.string(),
            instancePath: z.string().startsWith("/"),
            schemaPath: z.string(),
            message: z.string(),
            params: z
              .object({ issue: z.object({ code: z.string(), path: z.array(z.union([z.string(), z.number()])), message: z.string() }).strict() })
              .strict(),
          })
          .strict(),
      ),
    })
    .strict(),
}).strict();
// The repos DTO of a GitHub repository: the optional GitLab-era keys are always sent (provider github,
// host github.com, web_url https://github.com/<full_name>, connection_id null).
const githubRepo = Repo.extend({
  provider: z.literal("github"),
  host: z.literal("github.com"),
  web_url: z.string().startsWith("https://github.com/"),
  connection_id: z.null(),
}).strict();
// A connected GitLab repository: nested path, the GitLab web_url (<base_url>/<full_name>, here the host
// key is gitlab.example.com) and the id of the connection bound to its host key.
const gitlabRepo = Repo.extend({
  provider: z.literal("gitlab"),
  host: z.string().min(1),
  web_url: z.string().startsWith("https://"),
  connection_id: uuid,
}).strict();
// A connection with a bound repository: repo_count and disconnected_repos count it.
const gitlabConnectionBound = GitLabConnection.extend({ id: uuid, repo_count: z.number().int().positive() }).strict();
const gitlabConnectionDeletedBound = GitLabConnectionDeleted.extend({
  deleted: uuid,
  disconnected_repos: z.number().int().positive(),
}).strict();

// ---- Settings slice B (specs/2026-10-23-settings-b-workspace.md): rename, stats, export, reset. ----
const workspaceInfo = WorkspaceInfo.extend({
  workspaceId: uuid,
  repos: z.array(WorkspaceRepo.strict()),
}).strict();
// The reset answer lists every kind; `.strict()` on both levels makes a missing or extra kind a failure.
const workspaceResetResult = WorkspaceResetResult.extend({
  deleted: WorkspaceResetResult.shape.deleted.strict(),
}).strict();
const resetRunActive = ApiErrorBody.extend({
  error: ApiErrorBody.shape.error
    .extend({ code: z.literal(WORKSPACE_RESET_ACTIVE_CODE) })
    .strict(),
}).strict();
// The export is a plugin bundle of kind "workspace": the optional workspace-only parts are all present.
const workspaceExport = PluginBundle.extend({
  manifest: PluginManifest.extend({
    kind: z.literal("workspace"),
    description: z.string().nullable(),
    counts: PluginManifest.shape.counts.required().strict(),
  }).strict(),
  agents: z.array(PluginAgent.required({ strategy: true, ci_fail_on: true, repo_intel: true }).strict()),
  skills: z.array(
    PluginSkill.required({ versions: true })
      .extend({ versions: z.array(PluginSkillVersion.strict()) })
      .strict(),
  ),
  eval_cases: z.array(PluginEvalCase.strict()),
  conventions: z.array(PluginConvention.required({ repo: true, category: true, status: true }).strict()),
  learnings: z.array(PluginLearning.strict()),
  context_files: z.array(PluginContextFile.strict()),
  settings: z.record(z.unknown()),
}).strict();

// ---- Settings slice C (specs/2026-10-23-settings-c-automatic-reviews.md): the status line. ----
const automaticReviewsStatus = AutomaticReviewsStatus.strict();

// ---- Settings slice D (specs/2026-10-23-settings-integrations.md): plugins and the CI repositories. ----
const pluginCounts = PluginCounts.strict();
const installedPlugin = InstalledPlugin.extend({ counts: pluginCounts }).strict();
const pluginList = PluginList.extend({
  plugins: z.array(installedPlugin),
  last_export: PluginLastExport.extend({ counts: pluginCounts }).strict().nullable(),
}).strict();
const pluginPreview = PluginImportPreview.extend({
  manifest: PluginManifest.extend({ counts: PluginManifest.shape.counts.strict() }).strict(),
  counts: PluginPreviewCounts.strict(),
  agents: z.array(PluginImportPreview.shape.agents.element.strict()),
  skills: z.array(PluginImportPreview.shape.skills.element.strict()),
  conflicts: PluginImportPreview.shape.conflicts.strict(),
  ignored: z.array(PluginImportPreview.shape.ignored.element.strict()),
}).strict();
const pluginInstalled = PluginImportResult.extend({
  installed: installedPlugin,
  created: PluginImportResult.shape.created.strict(),
}).strict();
const pluginRemoved = PluginRemoveResult.extend({ removed: PluginRemoveResult.shape.removed.strict() }).strict();
const pluginDisabledError = ApiErrorBody.extend({
  error: ApiErrorBody.shape.error.extend({ code: z.literal(PLUGIN_DISABLED_CODE) }).strict(),
}).strict();
const pluginRef = PluginRef.strict();
const ciRepoInstallations = CiRepoInstallations.extend({
  repos: z.array(
    CiRepoInstallations.shape.repos.element
      .extend({
        installations: z.array(CiRepoInstallations.shape.repos.element.shape.installations.element.strict()),
        last_run: CiRepoInstallations.shape.repos.element.shape.last_run.unwrap().strict().nullable(),
      })
      .strict(),
  ),
}).strict();

// `.strict()` makes an extra key a failure: the Go API must not send fields the contract lacks.
const contracts: Record<string, z.ZodTypeAny> = {
  health: z.object({ status: z.literal("ok") }).strict(),
  health_ready: z.object({ ready: z.boolean() }).strict(),
  settings_get: Settings,
  settings_put: Settings,
  secrets_status: SecretsStatus.strict(),
  // The golden is a GitHub success: latency_ms, login and scopes (null = not reported) are present.
  test_connection: ConnTestResult.extend({
    latency_ms: z.number().int().nonnegative(),
    login: z.string().min(1),
    scopes: z.array(z.string()).nullable(),
  }).strict(),
  repos_create: githubRepo,
  repos_list: z.array(githubRepo),
  workspace: z
    .object({
      workspaceId: uuid,
      // Settings B: the display name (default when none is stored) and the data directory.
      name: z.string().min(1),
      dataDir: z.string().min(1),
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

  // ---- Phase 6: evals ----
  eval_case_manual_agent: agentEvalCase,
  eval_case_manual_skill: agentEvalCase,
  eval_case_finding: agentEvalCase,
  eval_case_updated: agentEvalCase,
  eval_cases_list: z.object({ cases: z.array(agentEvalCase) }).strict(),
  eval_case_list_empty: z.object({ cases: z.array(agentEvalCase) }).strict(),
  eval_run_started: evalRunSummary,
  eval_skill_run_started: evalRunSummary,
  eval_runs_list: evalRunList,
  eval_skill_runs_list: evalRunList,
  eval_run_detail: evalRunDetail,
  eval_run_all: evalRunAll,
  eval_compare: evalCompare,
  eval_overview: evalOverview,
  eval_overview_empty: evalOverview,
  eval_agent_dashboard: evalAgentDashboard,
  eval_agent_dashboard_7d: evalAgentDashboard,
  eval_agent_dashboard_empty: evalAgentDashboard,

  // ---- Phase 6: export-to-CI ----
  ci_export_files: ciExport,
  ci_export_open_pr: ciExport,
  ci_export_files_custom: ciExport,
  ci_export_files_openai: ciExport,
  ci_export_files_anthropic: ciExport,
  ci_export_open_pr_custom: ciExport,
  ci_installations: z.object({ installations: z.array(ciInstallation) }).strict(),
  ci_runs_all: ciRuns,
  ci_runs_agent: ciRuns,
  ci_runs_after_agent_delete: ciRuns,
  ci_refresh_runs: ciRuns,
  ci_refresh: ciRefresh,
  ci_refresh_skips: ciRefresh,
  ci_refresh_errors: ciRefresh,
  ci_refresh_not_checked: ciRefresh,

  // ---- PR Leaderboard ----
  leaderboard_week: leaderboard,
  leaderboard_repo_30d: leaderboard,
  leaderboard_empty: leaderboard,

  // ---- Settings slice A ----
  about,
  settings_spend: spendStatus,
  test_connection_llm: testConnectionLlm,
  spend_cap_reached: spendCapReached,

  // ---- Settings slice B ----
  workspace_rename: workspaceInfo,
  workspace_stats: WorkspaceStats.strict(),
  workspace_export: workspaceExport,
  workspace_reset: workspaceResetResult,
  workspace_reset_run_active: resetRunActive,

  // ---- Settings slice C ----
  automatic_reviews_status: automaticReviewsStatus,

  // ---- Settings slice D ----
  plugins_list: pluginList,
  plugins_import_preview: pluginPreview,
  plugins_import_installed: pluginInstalled,
  plugins_patch: installedPlugin,
  plugins_delete: pluginRemoved,
  plugin_disabled: pluginDisabledError,
  ci_installations_by_repo: ciRepoInstallations,
  // An agent and a skill of a plugin carry its owner; the plugin object is strict here.
  agent_get_plugin: Agent.extend({ plugin: pluginRef }).strict(),
  skill_get_plugin: Skill.extend({ plugin: pluginRef }).strict(),

  // ---- GitLab connections ----
  gitlab_conn_create: gitlabConnection,
  gitlab_conn_create_no_token: gitlabConnection,
  gitlab_conn_update: gitlabConnection,
  gitlab_conn_list: z.array(gitlabConnection),
  gitlab_conn_deleted: gitlabConnectionDeleted,
  gitlab_conn_exists: gitlabConnError("gitlab_connection_exists"),
  gitlab_conn_not_found: gitlabConnError("gitlab_connection_not_found"),
  gitlab_conn_invalid: gitlabConnInvalid,
  gitlab_conn_test_ok: gitlabConnTest,
  gitlab_conn_test_warnings: gitlabConnTest,
  gitlab_conn_test_unknown: gitlabConnTest,
  gitlab_conn_test_rejected: gitlabConnTest,
  gitlab_conn_test_no_scope: gitlabConnTest,
  gitlab_conn_list_bound: z.array(gitlabConnectionBound).length(1),
  gitlab_conn_deleted_bound: gitlabConnectionDeletedBound,
  gitlab_repo_create: gitlabRepo,
  gitlab_repos_list: z.array(gitlabRepo).length(1),

  // ---- Custom model providers ----
  custom_providers_create: customProvider,
  custom_providers_create_anthropic: customProvider,
  custom_providers_update: customProvider,
  custom_providers_list: z.array(customProvider),
  custom_providers_deleted: CustomProviderDeleted.extend({ deleted: z.string().regex(/^custom:[a-z0-9-]{1,32}$/) }).strict(),
  custom_providers_in_use: customProviderInUse,
  // 422 validation_error: details are the httpx issues (params.issue holds the Zod-style issue).
  custom_providers_invalid: ApiErrorBody.extend({
    error: ApiErrorBody.shape.error
      .extend({
        code: z.literal("validation_error"),
        details: z.array(
          z
            .object({
              keyword: z.string(),
              instancePath: z.string().startsWith("/"),
              schemaPath: z.string(),
              message: z.string(),
              params: z
                .object({ issue: z.object({ code: z.string(), path: z.array(z.union([z.string(), z.number()])), message: z.string() }).strict() })
                .strict(),
            })
            .strict(),
        ),
      })
      .strict(),
  }).strict(),
  custom_providers_models: z.array(
    ModelInfo.extend({ provider: z.string().regex(/^custom:[a-z0-9-]{1,32}$/), pricing: ModelPricing.strict().nullish() }).strict(),
  ),
  custom_providers_test_ok: customProviderTest.extend({ model_reachable: z.boolean(), warning: z.undefined() }).strict(),
  custom_providers_test_warning: customProviderTest
    .extend({ model_reachable: z.null(), warning: z.literal("Model list not available; key not verified") })
    .strict(),
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

  it("settings carry the four seeded keys and the automatic-reviews object (Settings fills defaults, so check the raw body)", () => {
    expect(Object.keys(read("settings_get") as object).sort()).toEqual([
      "automatic_reviews",
      "density",
      "polling_interval_min",
      "sync_to_folder",
      "theme",
    ]);
    // GET /settings always carries the full object, off by default, with both triggers ticked.
    expect(read("settings_get")).toMatchObject({
      automatic_reviews: { enabled: false, agent_ids: [], on_new_pr: true, on_new_commits: true, enabled_at: null },
    });
    // A PUT of the legacy boolean answers the object; without an agent it is not armed (enabled_at null).
    expect(read("settings_put")).toMatchObject({
      automatic_reviews: { enabled: true, agent_ids: [], enabled_at: null },
    });
  });

  it("the settings-slice-A goldens carry what the UI reads", () => {
    const llm = read("test_connection_llm") as Record<string, unknown>;
    for (const key of ["login", "scopes"]) expect(llm).not.toHaveProperty(key);
    expect(llm).toMatchObject({ provider: "openrouter", ok: true, model_reachable: true });

    const spend = SpendStatus.parse(read("settings_spend"));
    expect(spend.cap_reached).toBe(false);
    expect(spend.remaining_usd).toBe((spend.cap_usd ?? 0) - spend.spent_usd);

    const refused = spendCapReached.parse(read("spend_cap_reached"));
    expect(refused.error.details.spent_usd).toBeGreaterThanOrEqual(refused.error.details.cap_usd);
  });

  it("the settings-slice-B goldens carry what the UI reads", () => {
    const info = workspaceInfo.parse(read("workspace_rename"));
    expect(info.name).toBe("acme · payments");
    expect(info.dataDir.startsWith("/")).toBe(true);

    const bundle = workspaceExport.parse(read("workspace_export"));
    expect(bundle.manifest.kind).toBe("workspace");
    const { counts } = bundle.manifest;
    expect(counts).toEqual({
      agents: bundle.agents.length,
      skills: bundle.skills.length,
      eval_cases: bundle.eval_cases.length,
      conventions: bundle.conventions.length,
      learnings: bundle.learnings.length,
      context_files: bundle.context_files.length,
    });
    // Duplicate agent names are suffixed, an imported skill keeps its source and all its versions,
    // and every eval case points at a bundle agent or skill.
    expect(bundle.agents.map((a) => a.name)).toEqual(["Reviewer", "Reviewer (2)", "Auditor"]);
    const imported = bundle.skills.find((x) => x.source === "imported_file");
    expect(imported?.versions.map((v) => v.version)).toEqual([1, 2, 3]);
    for (const c of bundle.eval_cases) {
      const owners = c.owner_kind === "agent" ? bundle.agents : bundle.skills;
      expect(owners.map((o) => o.name)).toContain(c.owner_ref);
    }
    // The settings are the allow-list only: no ids, no unknown keys, no workspace name.
    expect(Object.keys(bundle.settings).sort()).toEqual(["automatic_reviews", "retention_days", "theme"]);
    // The export never carries agent_ids or enabled_at (they are workspace-local).
    expect(bundle.settings.automatic_reviews).toEqual({ enabled: true, on_new_pr: true, on_new_commits: true });
    // The same document is a valid plain PluginBundle for the importer's schema.
    expect(PluginBundle.safeParse(read("workspace_export")).success).toBe(true);

    expect(read("workspace_stats")).toEqual({ agents: 3, skills: 6, eval_cases: 12, learnings: 24, repos: 2, runs: 7 });
    const reset = workspaceResetResult.parse(read("workspace_reset"));
    expect(reset.deleted.agent_runs).toBe(2);
    expect(reset.prs_unmarked).toBe(2);
    expect(resetRunActive.parse(read("workspace_reset_run_active")).error.message).toMatch(/in progress/);
  });

  it("the settings-slice-C status golden is an active line with a poll time and the tracked PRs", () => {
    const status = automaticReviewsStatus.parse(read("automatic_reviews_status"));
    expect(status.state).toBe("active");
    expect(status.last_poll_at).not.toBeNull();
    expect(status.prs_tracked).toBeGreaterThan(0);
    expect(status.poll_interval_min).toBe(5);
  });

  it("the settings-slice-D goldens carry what the UI reads", () => {
    // The install answer is the full row, disabled, with the counts the list shows afterwards.
    const installed = pluginInstalled.parse(read("plugins_import_installed"));
    expect(installed.installed.enabled).toBe(false);
    expect(installed.created.conventions).toBe(0);
    expect(installed.created).toEqual({ agents: 2, skills: 3, eval_cases: 2, conventions: 0, learnings: 2 });
    const list = pluginList.parse(read("plugins_list"));
    expect(list.plugins).toEqual([installed.installed]);
    expect(list.last_export).not.toBeNull();
    expect(installedPlugin.parse(read("plugins_patch"))).toEqual({ ...installed.installed, enabled: true });
    expect(pluginRemoved.parse(read("plugins_delete")).removed).toEqual({
      agents: 2,
      skills: 3,
      eval_cases: 2,
      learnings: 2,
      ci_installations: 0,
    });

    // The preview names the conflicting skill, lists the extra archive entry and keeps link order.
    const preview = pluginPreview.parse(read("plugins_import_preview"));
    expect(preview.conflicts).toEqual({ plugin: false, skills: ["style-guide"] });
    expect(preview.ignored.map((i) => i.path_or_section)).toEqual(["README.md"]);
    expect(preview.agents[0]?.skills).toEqual(["style-guide"]);

    // The 409 names the plugin; the owner shows on the agent and skill answers.
    expect(pluginDisabledError.parse(read("plugin_disabled")).error.message).toMatch(/disabled plugin "tools-pack"/);
    expect(Agent.parse(read("agent_get_plugin")).plugin).toMatchObject({ name: "tools-pack", enabled: false });
    expect(Skill.parse(read("skill_get_plugin")).plugin).toMatchObject({ name: "tools-pack", enabled: false });
  });

  it("the CI repositories golden lists repositories in byte order with the newest run", () => {
    const { repos } = ciRepoInstallations.parse(read("ci_installations_by_repo"));
    expect(repos.map((r) => r.repo)).toEqual(["acme/api", "acme/web"]);
    expect(repos[0]?.last_run).toMatchObject({ status: "succeeded" });
    expect(repos[1]?.last_run).toBeNull();
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

  it("the eval start goldens are running agent and skill runs with no metrics yet", () => {
    for (const name of ["eval_run_started", "eval_skill_run_started"]) {
      expect(read(name), name).toMatchObject({
        status: "running",
        finished_at: null,
        recall: null,
        precision: null,
        citation_accuracy: null,
        cost_usd: null,
        duration_ms: null,
      });
    }
    expect(read("eval_run_started")).toMatchObject({ owner_kind: "agent", skill_id: null, skill_version: null });
    const skillRun = read("eval_skill_run_started") as { skill_id: unknown; skill_version: unknown };
    expect(uuid.safeParse(skillRun.skill_id).success).toBe(true);
    expect(skillRun).toMatchObject({ owner_kind: "skill", skill_version: 1 });
  });

  it("the eval case goldens: an agent case, a skill case without an agent, a finding case and an edited one", () => {
    expect(read("eval_case_manual_agent")).toMatchObject({ owner_kind: "agent", origin: "manual", content_edited_at: null });
    expect(read("eval_case_manual_skill")).toMatchObject({ owner_kind: "skill", agent_id: null, origin: "manual" });
    const fromFinding = read("eval_case_finding") as Record<string, unknown>;
    expect(fromFinding.origin).toBe("finding");
    expect(uuid.safeParse(fromFinding.source_finding_id).success).toBe(true);
    expect(read("eval_case_updated")).toMatchObject({ content_edited_at: expect.any(String) });
    const list = read("eval_cases_list") as { cases: { origin: string }[] };
    expect(list.cases.map((c) => c.origin)).toEqual(["manual", "manual"]);
    expect(read("eval_case_list_empty")).toEqual({ cases: [] });
  });

  it("the eval run detail is a completed run with one result per case, passed and failed", () => {
    const detail = read("eval_run_detail") as { run: { status: string; cases_total: number }; results: { status: string }[] };
    expect(detail.run.status).toBe("completed");
    expect(detail.results).toHaveLength(detail.run.cases_total);
    expect(new Set(detail.results.map((r) => r.status))).toEqual(new Set(["passed", "failed"]));
  });

  it("run-all starts runs and skips agents with a reason; compare flags the flipped and absent cases", () => {
    const all = read("eval_run_all") as { started: { status: string }[]; skipped: { reason: string }[] };
    expect(all.started.every((r) => r.status === "running")).toBe(true);
    expect(new Set(all.skipped.map((s) => s.reason))).toEqual(new Set(["no_cases", "run_active"]));

    const compare = read("eval_compare") as { cases: { a: string; b: string; flipped: boolean }[] };
    expect(compare.cases.map((c) => [c.a, c.b, c.flipped])).toEqual([
      ["passed", "failed", true],
      ["failed", "failed", false],
      ["absent", "passed", true],
    ]);
  });

  it("the eval dashboards: an empty workspace and agent, a regression alert and the 7d window", () => {
    expect(read("eval_overview_empty")).toEqual({ agents: [], recent_runs: [] });
    expect(read("eval_agent_dashboard_empty")).toMatchObject({
      cases_total: 0,
      latest: null,
      previous: null,
      trend: [],
      alert: null,
    });
    for (const name of ["eval_agent_dashboard", "eval_agent_dashboard_7d"]) {
      const dashboard = read(name) as { alert: { metrics: { metric: string }[] } | null; trend: unknown[] };
      expect(dashboard.alert?.metrics.map((m) => m.metric), name).toEqual(["recall"]);
      expect(dashboard.trend.length, name).toBeGreaterThan(0);
    }
    const overview = read("eval_overview") as { agents: { sparkline: unknown[] }[] };
    expect(overview.agents.some((a) => a.sparkline.length === 10)).toBe(true);
  });

  it("the CI export goldens: files persist nothing, open_pr answers an installation and the PR url", () => {
    const files = read("ci_export_files") as { installation: unknown; pr_url: unknown; files: unknown[] };
    expect(files.installation).toBeNull();
    expect(files.pr_url).toBeNull();
    expect(files.files.length).toBeGreaterThan(0);
    const openPr = read("ci_export_open_pr") as { installation: Record<string, unknown> | null; pr_url: unknown };
    expect(openPr.installation).toMatchObject({ slug: "ci-reviewer", target_type: "gha" });
    expect(openPr.pr_url).toEqual(expect.stringMatching(/^https:\/\/github\.com\/.+\/pull\/\d+$/));
  });

  it("the CI export goldens of the other providers list their secrets and variables", () => {
    type Listed = { required_secrets: string[]; required_variables: string[] };
    expect(read("ci_export_files") as Listed).toMatchObject({
      required_secrets: ["OPENROUTER_API_KEY"],
      required_variables: [],
    });
    expect(read("ci_export_files_openai") as Listed).toMatchObject({
      required_secrets: ["OPENAI_API_KEY"],
      required_variables: [],
    });
    expect(read("ci_export_files_anthropic") as Listed).toMatchObject({
      required_secrets: ["ANTHROPIC_API_KEY"],
      required_variables: [],
    });
    for (const name of ["ci_export_files_custom", "ci_export_open_pr_custom"]) {
      expect(read(name) as Listed, name).toMatchObject({
        required_secrets: ["DEVDIGEST_MY_GW_API_KEY", "DEVDIGEST_MY_GW_HEADER_1", "DEVDIGEST_MY_GW_HEADER_2"],
        required_variables: ["DEVDIGEST_MY_GW_BASE_URL"],
      });
    }
    expect((read("ci_export_open_pr_custom") as { installation: unknown }).installation).toMatchObject({
      target_type: "gha",
    });
  });

  it("the CI runs goldens: newest first lists, and runs of a deleted agent keep a null agent", () => {
    const orphan = (read("ci_runs_after_agent_delete") as { runs: Record<string, unknown>[] }).runs[0];
    expect(orphan).toMatchObject({ agent: null, agent_id: null, ci_installation_id: null, verdict: null });
    for (const name of ["ci_runs_all", "ci_runs_agent", "ci_refresh_runs"]) {
      expect((read(name) as { runs: unknown[] }).runs.length, name).toBeGreaterThan(0);
    }
  });

  it("the CI refresh goldens cover every installation state, error code and skip reason", () => {
    const names = ["ci_refresh", "ci_refresh_skips", "ci_refresh_errors", "ci_refresh_not_checked"];
    const installations = names.flatMap(
      (n) =>
        (read(n) as { installations: { state: string; error_code?: string; skipped: { reason: string }[] }[] })
          .installations,
    );
    expect(new Set(installations.map((i) => i.state))).toEqual(new Set(["ok", "not_installed", "error", "not_checked"]));
    expect(new Set(installations.flatMap((i) => (i.error_code ? [i.error_code] : [])))).toEqual(
      new Set(["github_permission_denied", "github_rate_limited", "github_unavailable"]),
    );
    expect(new Set(installations.flatMap((i) => i.skipped.map((s) => s.reason)))).toEqual(
      new Set(["artifact_expired", "artifact_too_large", "artifact_invalid", "artifact_mismatch"]),
    );
    expect(read("ci_refresh_errors")).toMatchObject({ degraded: true, ingested: 0 });
    expect(read("ci_refresh_not_checked")).toMatchObject({ degraded: true });
    expect(read("ci_refresh")).toMatchObject({ degraded: false });
  });

  // These three goldens hold real fixed ids and times (the backend does not run them through its
  // placeholder normalizer), so the wire rules of contracts/leaderboard.ts are checked on the raw values.
  describe("leaderboard goldens", () => {
    type Board = {
      period: Record<string, string | null>;
      repos: { id: string; full_name: string }[];
      previous_summary: unknown;
      awards: Record<string, { repo: string; repo_id: string } | null>;
      rows: { repo: string; repo_id: string; opened_at: string | null; merged_at: string | null }[];
      needs_attention: { total: number; items: { repo: string }[] };
      hot_files: { repo: string; repo_id: string }[];
      health_by_size: { size: string }[];
      activity: { granularity: string; points: { from: string; to: string }[] };
    };
    const localTime = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}[+-]\d{2}:\d{2}$/;
    const utcTime = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/;
    const localDate = /^\d{4}-\d{2}-\d{2}$/;
    const names = ["leaderboard_week", "leaderboard_repo_30d", "leaderboard_empty"];

    it.each(names)("%s: period times are local with an offset, other times UTC, dates local", (name) => {
      const board = read(name) as Board;
      for (const key of ["from", "to"]) expect(board.period[key], key).toMatch(localTime);
      for (const key of ["previous_from", "previous_to"]) {
        const value = board.period[key];
        if (value !== null) expect(value, key).toMatch(localTime);
      }
      for (const row of board.rows) {
        for (const value of [row.opened_at, row.merged_at]) if (value !== null) expect(value).toMatch(utcTime);
      }
      expect(board.activity.points.length).toBeGreaterThan(0);
      for (const point of board.activity.points) {
        expect(point.from).toMatch(localDate);
        expect(point.to).toMatch(localDate);
      }
      expect(board.health_by_size.map((b) => b.size)).toEqual(["XS", "S", "M", "L", "XL"]);
    });

    it.each(names)("%s: repo fields are the short name, repos[].full_name is owner/name", (name) => {
      const board = read(name) as Board;
      const byId = new Map(board.repos.map((r) => [r.id, r.full_name]));
      for (const repo of board.repos) expect(repo.full_name).toMatch(/^[^/]+\/[^/]+$/);
      const linked = [
        ...board.rows,
        ...board.hot_files,
        ...Object.values(board.awards).filter((a) => a !== null),
      ];
      for (const item of linked) {
        expect(item.repo, item.repo).not.toContain("/");
        expect(byId.get(item.repo_id)?.endsWith(`/${item.repo}`), item.repo).toBe(true);
      }
      for (const item of board.needs_attention.items) expect(item.repo).not.toContain("/");
    });

    it("the week golden is a day-granular window with a previous window and rows", () => {
      const board = read("leaderboard_week") as Board;
      expect(board.period).toMatchObject({ key: "week", tz: "UTC" });
      expect(board.period.previous_from).not.toBeNull();
      expect(board.previous_summary).not.toBeNull();
      expect(board.activity.granularity).toBe("day");
      expect(board.activity.points).toHaveLength(7);
      expect(board.rows.length).toBeGreaterThan(0);
      expect(board.repos).toHaveLength(3);
    });

    it("the repo + 30d golden has no previous window (D19), week granularity and one repo", () => {
      const board = read("leaderboard_repo_30d") as Board;
      expect(board.period).toMatchObject({ key: "30d", tz: "Europe/Kyiv", previous_from: null, previous_to: null });
      expect(board.previous_summary).toBeNull();
      expect(board.activity.granularity).toBe("week");
      expect(board.repos).toHaveLength(1);
      expect(new Set(board.rows.map((r) => r.repo_id))).toEqual(new Set([board.repos[0]?.id]));
    });

    it("the empty golden has no rows, null awards, an empty attention list and a previous window", () => {
      const board = read("leaderboard_empty") as Board;
      expect(board.rows).toEqual([]);
      expect(Object.values(board.awards).every((a) => a === null)).toBe(true);
      expect(board.needs_attention).toEqual({ total: 0, items: [] });
      expect(board.hot_files).toEqual([]);
      expect(board.period.previous_from).not.toBeNull();
      expect(board.previous_summary).toBeNull();
    });
  });
});

describe("GitLab connection goldens (GitLab A, NFR7)", () => {
  type GitLabRepoGolden = {
    id: string;
    owner: string;
    name: string;
    full_name: string;
    host: string;
    web_url: string;
    connection_id: string;
    clone_path: string | null;
  };
  type Test = {
    ok: boolean;
    latency_ms: number | null;
    login: string | null;
    scopes: string[] | null;
    token_access: string | null;
    expires_at: string | null;
    instance_version: string | null;
    warnings: { code: string }[];
  };
  const testNames = [
    "gitlab_conn_test_ok",
    "gitlab_conn_test_warnings",
    "gitlab_conn_test_unknown",
    "gitlab_conn_test_rejected",
    "gitlab_conn_test_no_scope",
  ];

  it("no connection golden has a token key or a token-shaped value: the token is write-only", () => {
    const names = readdirSync(goldenDir)
      .filter((f) => f.startsWith("gitlab_conn_") && f.endsWith(".json"))
      .map((f) => f.slice(0, -".json".length));
    expect(names).toHaveLength(15);
    for (const name of names) {
      expect(JSON.stringify(read(name)), name).not.toMatch(/"token":|glpat-|oauth2/);
    }
  });

  it("connections are normalised: https base_url without /api/v4, host key of that URL, name within the limit", () => {
    const rows = [
      ...(read("gitlab_conn_list") as { base_url: string; host: string; name: string }[]),
      read("gitlab_conn_create") as { base_url: string; host: string; name: string },
      read("gitlab_conn_create_no_token") as { base_url: string; host: string; name: string },
      read("gitlab_conn_update") as { base_url: string; host: string; name: string },
    ];
    for (const row of rows) {
      const url = new URL(row.base_url);
      expect(url.protocol).toBe("https:");
      expect(row.base_url.endsWith("/")).toBe(false);
      expect(row.base_url).not.toContain("/api/v4");
      expect(url.host).toBe(row.host);
      expect(row.name.length).toBeLessThanOrEqual(GITLAB_CONNECTION_LIMITS.nameMax);
    }
  });

  it("create without a name falls back to the host key; create without a token reports token_configured false", () => {
    const withToken = read("gitlab_conn_create") as { name: string; host: string; token_configured: boolean };
    expect(withToken.name).toBe(withToken.host);
    expect(withToken.token_configured).toBe(true);
    expect((read("gitlab_conn_create_no_token") as { token_configured: boolean }).token_configured).toBe(false);
  });

  it("the delete answer names the deleted id of the created connection", () => {
    expect((read("gitlab_conn_deleted") as { deleted: string }).deleted).toBe((read("gitlab_conn_create") as { id: string }).id);
  });

  it("a successful test has a latency and a login; a failed one has every detail key null and no warnings", () => {
    for (const name of testNames) {
      const t = read(name) as Test;
      if (t.ok) {
        expect(t.latency_ms, name).not.toBeNull();
        expect(t.login, name).toBeTruthy();
      } else {
        expect(
          [t.latency_ms, t.login, t.scopes, t.token_access, t.expires_at, t.instance_version],
          name,
        ).toEqual([null, null, null, null, null, null]);
        expect(t.warnings, name).toEqual([]);
      }
    }
  });

  it("token_access follows the scopes: api = full, read_api only = read_only, unknown scopes = null", () => {
    const access = (t: Test): string | null =>
      t.scopes === null ? null : t.scopes.includes("api") ? "full" : t.scopes.includes("read_api") ? "read_only" : null;
    for (const name of ["gitlab_conn_test_ok", "gitlab_conn_test_warnings", "gitlab_conn_test_unknown"]) {
      const t = read(name) as Test;
      expect(t.token_access, name).toBe(access(t));
    }
  });

  it("warnings come in the FR11 order, without repeats, and the unknown golden lists both unknowns", () => {
    for (const name of testNames) {
      const codes = (read(name) as Test).warnings.map((w) => GITLAB_TEST_WARNING_CODES.indexOf(w.code as never));
      expect(codes.every((i) => i >= 0), name).toBe(true);
      expect(codes, name).toEqual([...codes].sort((a, b) => a - b));
      expect(new Set(codes).size, name).toBe(codes.length);
    }
    expect((read("gitlab_conn_test_unknown") as Test).warnings.map((w) => w.code)).toEqual(["scopes_unknown", "version_unknown"]);
    expect((read("gitlab_conn_test_warnings") as Test).warnings.map((w) => w.code)).toEqual([
      "read_only_token",
      "no_repository_scope",
      "token_expiring",
      "version_unsupported",
    ]);
  });

  it("the validation golden names the fields by JSON pointer: /base_url and /token", () => {
    const body = read("gitlab_conn_invalid") as { error: { details: { instancePath: string }[] } };
    expect(body.error.details.map((d) => d.instancePath)).toEqual(["/base_url", "/token"]);
  });

  it("a bound connection counts its repository; deleting it disconnects that many and names its id", () => {
    const [conn] = read("gitlab_conn_list_bound") as [{ id: string; repo_count: number; host: string }];
    const deleted = read("gitlab_conn_deleted_bound") as { deleted: string; disconnected_repos: number };
    expect(deleted.deleted).toBe(conn.id);
    expect(deleted.disconnected_repos).toBe(conn.repo_count);
  });

  it("the GitLab repo goldens: nested path, GitLab web_url, bound to the listed connection", () => {
    const created = read("gitlab_repo_create") as GitLabRepoGolden;
    const [listed] = read("gitlab_repos_list") as [GitLabRepoGolden];
    const [conn] = read("gitlab_conn_list_bound") as [{ id: string; host: string; base_url: string }];
    for (const r of [created, listed]) {
      expect(r.full_name.split("/").length, r.full_name).toBeGreaterThan(2);
      expect(r.owner, r.full_name).toBe(r.full_name.slice(0, r.full_name.lastIndexOf("/")));
      expect(r.name).toBe(r.full_name.slice(r.full_name.lastIndexOf("/") + 1));
      expect(r.host).toBe(conn.host);
      expect(r.web_url).toBe(`${conn.base_url}/${r.full_name}`);
      expect(r.connection_id).toBe(conn.id);
    }
    expect(listed.id).toBe(created.id);
    expect(created.clone_path).toBeNull();
    expect(listed.clone_path).toContain(`/@gitlab/${conn.host}/`);
  });

  it("the repos goldens are GitHub repos: web_url is https://github.com/<full_name>", () => {
    const repos = [read("repos_create") as { full_name: string; web_url: string }, ...(read("repos_list") as { full_name: string; web_url: string }[])];
    for (const r of repos) expect(r.web_url).toBe(`https://github.com/${r.full_name}`);
  });
});
