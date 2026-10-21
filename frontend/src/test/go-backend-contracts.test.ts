import { readFileSync, readdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { z } from "zod";
import {
  ConnTestResult,
  PrDetail,
  PrMeta,
  PrReviewComment,
  Repo,
  SecretsStatus,
  Settings,
} from "@devdigest/shared";

// The Go backend's component tests record the response of every Phase-1 route in
// backend/cmd/server/testdata/contracts. This file parses each recording with the vendored
// Zod contracts the UI uses, so a drift on either side fails here. The contracts themselves
// are never edited (frontend/AGENTS.md: src/vendor/shared is the wire contract).
const goldenDir = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "../../../backend/cmd/server/testdata/contracts",
);

const uuid = z.string().uuid();
const nullableString = z.string().nullable();

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

  it("settings carry the four seeded keys (Settings fills defaults, so check the raw body)", () => {
    expect(Object.keys(read("settings_get") as object).sort()).toEqual([
      "density",
      "polling_interval_min",
      "sync_to_folder",
      "theme",
    ]);
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
