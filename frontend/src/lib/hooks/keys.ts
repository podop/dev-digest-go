/* hooks/keys.ts — the one query-key scheme for every TanStack Query hook.
   Keys are hierarchical, so invalidating a prefix covers its children:
     prKeys.detail(prId)   → the PR itself + its runs, active runs, reviews, comments
     repoKeys.detail(id)   → a repo's pulls list, context files and index state
     skillKeys.detail(id)  → a skill + its versions and stats
   Hooks and mutations reference these factories — never inline key literals. */

type Id = string | number | null | undefined;

export const settingsKeys = {
  all: ["settings"] as const,
  current: () => [...settingsKeys.all, "current"] as const,
  secretsStatus: () => [...settingsKeys.all, "secrets-status"] as const,
  /** GET /settings/spend — month-to-date spend vs the cap. */
  spend: () => [...settingsKeys.all, "spend"] as const,
  /** GET /settings/automatic-reviews-status — derived on read, refetched while mounted. */
  autoReviewsStatus: () => [...settingsKeys.all, "automatic-reviews-status"] as const,
};

/** GET /about — build/runtime facts; never changes while the server runs. */
export const aboutKeys = {
  all: ["about"] as const,
};

export const providerKeys = {
  all: ["providers"] as const,
  models: (provider: Id) => [...providerKeys.all, provider, "models"] as const,
};

export const repoKeys = {
  all: ["repos"] as const,
  list: () => [...repoKeys.all, "list"] as const,
  detail: (repoId: Id) => [...repoKeys.all, "detail", repoId] as const,
  pulls: (repoId: Id) => [...repoKeys.detail(repoId), "pulls"] as const,
  /** Project Context list of a repo (GET /repos/:id/context); also the prefix of its doc previews. */
  context: (repoId: Id) => [...repoKeys.detail(repoId), "context"] as const,
  /** One document preview (GET /repos/:id/context/doc?path=). */
  contextDoc: (repoId: Id, path: Id) => [...repoKeys.context(repoId), "doc", path] as const,
  intelState: (repoId: Id) => [...repoKeys.detail(repoId), "intel-state"] as const,
  /** Onboarding tour of a repo (GET/POST /repos/:id/onboarding). */
  onboarding: (repoId: Id) => [...repoKeys.detail(repoId), "onboarding"] as const,
};

export const prKeys = {
  all: ["pulls"] as const,
  /** Key of the PR detail query AND the prefix of everything scoped to that PR. */
  detail: (prId: Id) => [...prKeys.all, prId] as const,
  runs: (prId: Id) => [...prKeys.detail(prId), "runs"] as const,
  activeRuns: (prId: Id) => [...prKeys.detail(prId), "active-runs"] as const,
  reviews: (prId: Id) => [...prKeys.detail(prId), "reviews"] as const,
  comments: (prId: Id) => [...prKeys.detail(prId), "comments"] as const,
  /** GET /pulls/:id/intent (server/specs/05-intent-layer.md) — a review may derive/refresh it. */
  intent: (prId: Id) => [...prKeys.detail(prId), "intent"] as const,
  /** GET /pulls/:id/smart-diff (server/specs/06-smart-diff.md) — a review changes the newest one's finding lines. */
  smartDiff: (prId: Id) => [...prKeys.detail(prId), "smart-diff"] as const,
  /** GET /pulls/:id/blast (server/specs/07-blast-radius.md) — an index read; a resync invalidates it. */
  blast: (prId: Id) => [...prKeys.detail(prId), "blast"] as const,
  /** GET /pulls/:id/brief (specs/2026-10-01-pr-brief.md) — written only by its own POST; not run-scoped, a run changes the banner (reviews), not the stored brief text. */
  brief: (prId: Id) => [...prKeys.detail(prId), "brief"] as const,
  /** GET /pulls/:id/history — previously merged PRs touching the same files. */
  history: (prId: Id) => [...prKeys.detail(prId), "history"] as const,
};

/** PR-scoped children a finished run changes (used when the PR id is unknown). */
export const RUN_SCOPED_PR_KEYS = ["runs", "active-runs", "reviews", "intent", "smart-diff"] as const;

export const agentKeys = {
  all: ["agents"] as const,
  list: () => [...agentKeys.all, "list"] as const,
  detail: (id: Id) => [...agentKeys.all, "detail", id] as const,
  versions: (id: Id) => [...agentKeys.detail(id), "versions"] as const,
  /** Prefix of every agent's skill links (invalidate all after a skill delete). */
  skillsAll: () => [...agentKeys.all, "skills"] as const,
  /** The ordered skill links of one agent (GET /agents/:id/skills). */
  skills: (id: Id) => [...agentKeys.skillsAll(), id] as const,
  /** The ordered Project Context paths of one agent in one repo (GET /agents/:id/context). */
  context: (id: Id, repoId: Id) => [...agentKeys.contextAll(), id, repoId] as const,
  /** Prefix of every agent's Project Context paths (a store-file rename rewrites them all). */
  contextAll: () => [...agentKeys.all, "context"] as const,
};

/** Filters of the community catalog search (GET /skills/community). */
export interface CommunitySkillFilters {
  q?: string;
  tag?: string;
  lang?: string;
}

export const skillKeys = {
  all: ["skills"] as const,
  list: () => [...skillKeys.all, "list"] as const,
  /** Card numbers of every skill (GET /skills/stats). */
  stats: () => [...skillKeys.all, "stats"] as const,
  /** Key of the skill query AND the prefix of its versions + stats. */
  detail: (id: Id) => [...skillKeys.all, "detail", id] as const,
  versions: (id: Id) => [...skillKeys.detail(id), "versions"] as const,
  statsFor: (id: Id) => [...skillKeys.detail(id), "stats"] as const,
  /** Prefix of every skill's "used by" list (agent links change them all). */
  agentsAll: () => [...skillKeys.all, "agents"] as const,
  agents: (id: Id) => [...skillKeys.agentsAll(), id] as const,
  /** The ordered Project Context paths of one skill in one repo (GET /skills/:id/context). */
  context: (id: Id, repoId: Id) => [...skillKeys.contextAll(), id, repoId] as const,
  /** Prefix of every skill's Project Context paths (a store-file rename rewrites them all). */
  contextAll: () => [...skillKeys.all, "context"] as const,
  community: (f: CommunitySkillFilters) =>
    [...skillKeys.all, "community", f.q ?? "", f.tag ?? "", f.lang ?? ""] as const,
};

export const conventionKeys = {
  /** Latest scan + every rule of a repo (GET /repos/:id/conventions); nested under the repo. */
  state: (repoId: Id) => [...repoKeys.detail(repoId), "conventions"] as const,
};

/** Multi-agent review (specs/multi-agent-review.md §7). `run(id)` is one parent run's read
 *  model; `repoRuns` the per-repo list; `estimates` the per-agent time/cost averages. */
export const multiAgentKeys = {
  all: ["multi-agent"] as const,
  run: (id: Id) => [...multiAgentKeys.all, "run", id] as const,
  repoRuns: (repoId: Id, limit?: number) =>
    [...multiAgentKeys.all, "repo", repoId, ...(limit === undefined ? [] : [limit])] as const,
  estimates: () => [...multiAgentKeys.all, "estimates"] as const,
};

export const runKeys = {
  all: ["runs"] as const,
  trace: (runId: Id) => [...runKeys.all, runId, "trace"] as const,
};

/** Eval pipeline (specs/eval-pipeline.md §7). Everything agent-scoped nests under
 *  `agent(id)`, everything skill-scoped under `skill(id)`, so one prefix invalidation
 *  refreshes an owner's cases, runs and dashboard. */
export const evalKeys = {
  all: ["evals"] as const,
  agent: (agentId: Id) => [...evalKeys.all, "agent", agentId] as const,
  /** GET /agents/:id/eval-cases */
  cases: (agentId: Id) => [...evalKeys.agent(agentId), "cases"] as const,
  /** GET /agents/:id/eval-runs?limit=&period= — the prefix omits `limit`; `period` follows it. */
  runs: (agentId: Id, limit?: number, period?: string) =>
    [
      ...evalKeys.agent(agentId),
      "runs",
      ...(limit === undefined ? [] : [limit]),
      ...(period === undefined ? [] : [period]),
    ] as const,
  /** GET /agents/:id/eval/dashboard?period= — the prefix omits `period`. */
  dashboard: (agentId: Id, period?: string) =>
    [...evalKeys.agent(agentId), "dashboard", ...(period === undefined ? [] : [period])] as const,
  /** Everything skill-scoped: its cases and its runs (a skill's runs never share the agent prefix). */
  skill: (skillId: Id) => [...evalKeys.all, "skill", skillId] as const,
  /** GET /skills/:id/eval-cases */
  skillCases: (skillId: Id) => [...evalKeys.skill(skillId), "cases"] as const,
  /** GET /skills/:id/eval-runs?limit= — the prefix omits `limit`. */
  skillRuns: (skillId: Id, limit?: number) =>
    [...evalKeys.skill(skillId), "runs", ...(limit === undefined ? [] : [limit])] as const,
  /** GET /eval-runs/:id (run + per-case results). */
  run: (runId: Id) => [...evalKeys.all, "run", runId] as const,
  /** GET /eval-runs/compare?a=&b= */
  compare: (a: Id, b: Id) => [...evalKeys.all, "compare", a, b] as const,
  /** GET /eval/dashboard (all agents). */
  overview: () => [...evalKeys.all, "overview"] as const,
};

/** Export to CI (specs/export-to-ci.md). Installations and runs are plain lists; the
 *  generated-files preview is keyed by everything that changes the files. */
export const ciKeys = {
  all: ["ci"] as const,
  /** Prefix of every CI runs list (GET /ci/runs). */
  runsAll: () => [...ciKeys.all, "runs"] as const,
  /** GET /ci/runs?agent_id=&limit= — `agentId` null = every agent. */
  runs: (agentId: Id, limit?: number) => [...ciKeys.runsAll(), agentId ?? null, limit ?? null] as const,
  /** Prefix of every agent's installations list. */
  installationsAll: () => [...ciKeys.all, "installations"] as const,
  /** GET /agents/:id/ci-installations. */
  installations: (agentId: Id) => [...ciKeys.installationsAll(), agentId] as const,
  /** GET /ci/installations — installations grouped by repository (Settings › Integrations). */
  repoInstallations: () => [...ciKeys.all, "repo-installations"] as const,
  /** POST /agents/:id/export-ci { action: "files" } — the wizard's Preview. */
  exportFiles: (agentId: Id, repo: Id, triggers: readonly string[], postAs: Id) =>
    [...ciKeys.all, "export-files", agentId, repo, [...triggers].sort().join(","), postAs] as const,
};

/** Agent performance (specs/2026-10-16-agent-performance.md §7). `period` is the URL period
 *  (`periodSearch(...)`), so each period is its own cache entry. */
export const performanceKeys = {
  all: ["performance"] as const,
  /** GET /agents/performance?period=… */
  dashboard: (period: string) => [...performanceKeys.all, "dashboard", period] as const,
  /** GET /agents/:id/stats?period=… */
  agentStats: (agentId: Id, period: string) => [...performanceKeys.all, "agent", agentId, period] as const,
};

/** PR Leaderboard (specs/2026-10-23-pr-leaderboard.md §7). Period, repo filter and the browser
 *  zone are part of the key, so every combination is its own cache entry (no stale numbers). */
export const leaderboardKeys = {
  all: ["leaderboard"] as const,
  /** GET /leaderboard?period=&tz=&repo= */
  view: (period: string, repo: string, tz: string) => [...leaderboardKeys.all, "view", period, repo, tz] as const,
};

/** Settings › Workspace (specs/2026-10-23-settings-b-workspace.md §7). A reset invalidates the prefix. */
export const workspaceKeys = {
  all: ["workspace"] as const,
  /** GET /workspace — id, name, data dir, repos. */
  info: () => [...workspaceKeys.all, "info"] as const,
  /** GET /workspace/stats — the six counts. */
  stats: () => [...workspaceKeys.all, "stats"] as const,
};

/** Installed plugins (specs/2026-10-23-settings-integrations.md §7). Enabling, disabling, installing or
 *  removing one also changes the agent and skill lists (the plugin badge), so those mutations
 *  invalidate `agentKeys.all` and `skillKeys.all` too. */
export const pluginKeys = {
  all: ["plugins"] as const,
  /** GET /plugins — rows and the last export. */
  list: () => [...pluginKeys.all, "list"] as const,
};
