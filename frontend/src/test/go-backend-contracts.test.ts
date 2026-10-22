import { readFileSync, readdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { z } from "zod";
import {
  Agent,
  AgentSkillLink,
  AgentVersion,
  AgentVersionConfig,
  CommunitySkill,
  ConnTestResult,
  ContextAttachments,
  ContextDoc,
  ContextDocPreview,
  ContextList,
  Convention,
  ConventionScan,
  ModelInfo,
  PrDetail,
  PrMeta,
  PrReviewComment,
  Repo,
  RepoIndexState,
  SecretsStatus,
  Settings,
  Skill,
  SkillAgentRef,
  SkillImportPreview,
  SkillStats,
  SkillStatsSummary,
  SkillVersion,
} from "@devdigest/shared";

// The Go backend's component tests record the response of every Phase-1 and Phase-2 route in
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
});
