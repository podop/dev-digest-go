/**
 * @devdigest/shared — single source of truth for cross-package contracts.
 *
 * Exports (Zod schemas + inferred TS types):
 *  - contracts/findings   Review, Finding, Severity, Verdict, FindingAction, trifecta
 *  - contracts/brief      Intent, BlastRadius, Risks, PrHistory, SmartDiff, PrBrief
 *  - contracts/knowledge  Conformance, EvalRun/EvalCase, MemoryItem,
 *                         Skill/CommunitySkill, ConventionCandidate, Agent
 *  - contracts/eval-pipeline  AgentEvalCase, EvalRunSummary, EvalCompare, eval dashboards
 *  - contracts/trace      RunTrace, RunEvent, RunLogLine (single-document trace)
 *  - contracts/platform   Settings, ConnTestResult, Repo, PrMeta/PrDetail, …
 *  - contracts/project-context  ContextDoc, ContextList, ContextAttachments, RunTrace.project_context
 *  - contracts/onboarding  OnboardingTour, OnboardingTourState (GET/POST /repos/:id/onboarding)
 *  - contracts/leaderboard  Leaderboard (GET /leaderboard): rows, KPIs, awards, attention, charts
 *  - contracts/workspace  Settings › Workspace: WorkspaceInfo, WorkspaceStats, rename + reset bodies
 *  - constants/workspace  zod-free name rule, retention options, reset error codes
 *                         (re-exported by contracts/workspace; retention default by contracts/platform)
 *  - adapters             adapter interfaces + ModelInfo
 *  - constants/feature-models  zod-free FEATURE_MODELS registry (re-exported by
 *                         contracts/platform; import the subpath from bundles
 *                         that must stay zod-free)
 *  - constants/skills     zod-free skill limits + name regex (re-exported by
 *                         contracts/knowledge; same subpath rule)
 *  - constants/project-context  zod-free Project Context limits + default glob
 *                         (re-exported by contracts/project-context)
 *  - constants/onboarding  zod-free Onboarding Tour limits + section ids
 *                         (re-exported by contracts/onboarding)
 *  - constants/evals      zod-free eval case limits, period presets + trend cap
 *                         (re-exported by contracts/eval-pipeline)
 *  - constants/settings   zod-free spend-cap bounds + error code, settings keys, about runner kind
 *                         (cap bounds/code re-exported by contracts/platform)
 *
 * Feature agents (A1–A6) and F2 import everything from here. The barrel is
 * stable — feature agents EXTEND with new files, they do not edit existing ones.
 */

export * from './contracts/findings.js';
export * from './contracts/review-api.js';
export * from './contracts/brief.js';
export * from './contracts/knowledge.js';
export * from './contracts/trace.js';
export * from './contracts/platform.js';
export * from './contracts/why.js';
export * from './contracts/eval-ci.js';
export * from './contracts/eval-pipeline.js';
export * from './contracts/observability.js';
export * from './contracts/productionize.js';
export * from './contracts/project-context.js';
export * from './contracts/onboarding.js';
export * from './contracts/leaderboard.js';
export * from './contracts/workspace.js';
export * from './adapters.js';
