import { describe, it, expect } from "vitest";
import {
  Agent,
  CiRepoInstallations,
  InstalledPlugin,
  PluginExportRequest,
  PluginImportPreview,
  PluginImportResult,
  PluginList,
  PluginRemoveResult,
  Skill,
} from "@devdigest/shared";
import {
  PLUGIN_JSON_UPLOAD_MAX_BYTES,
  PLUGIN_ZIP_UPLOAD_MAX_BYTES,
  pluginUploadKind,
  pluginUploadMaxBytes,
  pluginUploadMediaType,
} from "@devdigest/shared/constants/plugins";
import { ciExportable, isPluginDisabled } from "./index";

const ON = { id: "p1", name: "acme", enabled: true };
const OFF = { id: "p2", name: "beta", enabled: false };

const COUNTS = { agents: 2, skills: 3, eval_cases: 4, learnings: 5 };
const ROW = {
  id: "p1",
  name: "acme",
  version: "1.0.0",
  source: "acme.zip",
  installed_at: "2026-10-26T10:00:00.000Z",
  enabled: false,
  description: null,
  counts: COUNTS,
};
const MANIFEST = {
  name: "acme",
  version: "1.0.0",
  format: "devdigest-plugin/v1",
  exported_at: "2026-10-26T10:00:00.000Z",
  counts: { agents: 1, skills: 1, eval_cases: 0, conventions: 0 },
};

describe("plugin helpers", () => {
  it("isPluginDisabled is true only for an item of a plugin that is switched off", () => {
    expect(isPluginDisabled({ plugin: OFF })).toBe(true);
    expect(isPluginDisabled({ plugin: ON })).toBe(false);
    expect(isPluginDisabled({ plugin: null })).toBe(false);
    expect(isPluginDisabled({})).toBe(false);
    expect(isPluginDisabled(undefined)).toBe(false);
  });

  it("ciExportable offers openrouter, openai, anthropic and defined custom agents of an enabled or no plugin, and says why not otherwise (FR32, EC34)", () => {
    const custom = [{ id: "custom:synapse", name: "Synapse" }];
    expect(ciExportable({ provider: "openrouter" })).toEqual({ ok: true });
    expect(ciExportable({ provider: "openrouter", plugin: ON })).toEqual({ ok: true });
    expect(ciExportable({ provider: "anthropic" }, custom)).toEqual({ ok: true });
    expect(ciExportable({ provider: "openai" }, [])).toEqual({ ok: true });
    expect(ciExportable({ provider: "custom:synapse" }, custom)).toEqual({ ok: true });
    expect(ciExportable({ provider: "custom:ghost" }, custom)).toEqual({ ok: false, reason: "custom_not_configured" });
    expect(ciExportable({ provider: "custom:synapse" }, [])).toEqual({ ok: false, reason: "custom_not_configured" });
    // The provider list is not known yet: the server decides.
    expect(ciExportable({ provider: "custom:ghost" })).toEqual({ ok: true });
    expect(ciExportable({ provider: "openrouter", plugin: OFF })).toEqual({ ok: false, reason: "plugin_disabled" });
    expect(ciExportable({ provider: "openai", plugin: OFF })).toEqual({ ok: false, reason: "plugin_disabled" });
    expect(ciExportable({ provider: "custom:synapse", plugin: OFF }, custom)).toEqual({ ok: false, reason: "plugin_disabled" });
    // A malformed id is not a provider CI knows.
    expect(ciExportable({ provider: "custom:Bad Id" as never }, custom)).toEqual({ ok: false, reason: "provider_not_supported" });
  });

  it("upload kind, media type and limit follow the file extension", () => {
    expect(pluginUploadKind("Acme.ZIP")).toBe("zip");
    expect(pluginUploadKind("bundle.json")).toBe("json");
    expect(pluginUploadKind("notes.txt")).toBeNull();
    expect(pluginUploadKind("zip")).toBeNull();
    expect(pluginUploadMediaType("zip")).toBe("application/zip");
    expect(pluginUploadMediaType("json")).toBe("application/json");
    expect(pluginUploadMaxBytes("zip")).toBe(PLUGIN_ZIP_UPLOAD_MAX_BYTES);
    expect(pluginUploadMaxBytes("json")).toBe(PLUGIN_JSON_UPLOAD_MAX_BYTES);
    expect(PLUGIN_ZIP_UPLOAD_MAX_BYTES).toBe(5 * 1024 * 1024);
    expect(PLUGIN_JSON_UPLOAD_MAX_BYTES).toBe(20 * 1024 * 1024);
  });
});

describe("plugin contracts (spec §7)", () => {
  it("agent and skill carry plugin as optional, null or {id,name,enabled}", () => {
    const agent = { id: "a", name: "A", description: "", provider: "openai", model: "m", system_prompt: "s", enabled: true, version: 1 };
    const skill = { id: "s", name: "s", description: "", type: "rubric", source: "manual", body: "b", enabled: true, version: 1 };
    expect(Agent.parse(agent).plugin).toBeUndefined();
    expect(Agent.parse({ ...agent, plugin: null }).plugin).toBeNull();
    expect(Agent.parse({ ...agent, plugin: OFF }).plugin).toEqual(OFF);
    expect(Skill.parse({ ...skill, plugin: ON }).plugin).toEqual(ON);
    expect(Skill.safeParse({ ...skill, plugin: { id: "p" } }).success).toBe(false);
  });

  it("the export request takes the §7 keys, enforces their limits and refuses unknown keys", () => {
    const id = "11111111-1111-4111-8111-111111111111";
    expect(PluginExportRequest.parse({})).toEqual({});
    expect(
      PluginExportRequest.parse({
        name: "acme-plugin",
        description: "d",
        agent_ids: [id],
        skill_ids: [id],
        include_eval_cases: false,
        include_learnings: true,
      }).include_learnings,
    ).toBe(true);
    for (const bad of [
      { name: "Not Kebab" },
      { name: "x".repeat(65) },
      { description: "x".repeat(301) },
      { agent_ids: [] },
      { agent_ids: ["not-a-uuid"] },
      { agent_ids: Array(201).fill(id) },
      { skill_ids: Array(501).fill(id) },
      { include_learnings: "yes" },
      { extra: 1 },
    ]) {
      expect(PluginExportRequest.safeParse(bad).success, JSON.stringify(bad).slice(0, 40)).toBe(false);
    }
  });

  it("the list, the row, the result and the removal parse the §7 shapes", () => {
    const last = { file_name: "acme-2026-10-26.zip", exported_at: "2026-10-26T10:00:00.000Z", counts: COUNTS };
    expect(PluginList.parse({ plugins: [ROW], last_export: last })).toEqual({ plugins: [ROW], last_export: last });
    expect(PluginList.parse({ plugins: [], last_export: null }).last_export).toBeNull();
    expect(InstalledPlugin.parse({ ...ROW, version: null, source: null })).toMatchObject({ version: null, source: null });
    expect(InstalledPlugin.safeParse({ ...ROW, counts: undefined }).success).toBe(false);
    const result = { installed: ROW, created: { agents: 2, skills: 3, eval_cases: 4, conventions: 0, learnings: 5 } };
    expect(PluginImportResult.parse(result)).toEqual(result);
    const removed = { ok: true, removed: { ...COUNTS, ci_installations: 1 } };
    expect(PluginRemoveResult.parse(removed)).toEqual(removed);
    expect(PluginRemoveResult.safeParse({ ok: false, removed: removed.removed }).success).toBe(false);
  });

  it("the preview parses the dry-run shape and rejects an unknown kind", () => {
    const preview = {
      manifest: MANIFEST,
      kind: "plugin",
      counts: { agents: 1, skills: 1, eval_cases: 0, conventions: 0, learnings: 0 },
      agents: [{ name: "A", provider: "openrouter", model: "m", enabled: true, skills: ["s"] }],
      skills: [{ name: "s", type: "rubric", description: "", enabled: true }],
      conflicts: { plugin: false, skills: [] },
      ignored: [{ path_or_section: "README.md", reason: "not part of the bundle" }],
      warnings: [],
    };
    expect(PluginImportPreview.parse(preview)).toEqual(preview);
    expect(PluginImportPreview.safeParse({ ...preview, kind: "other" }).success).toBe(false);
  });

  it("GET /ci/installations parses repos with installations and a nullable last run", () => {
    const body = {
      repos: [
        {
          repo: "o/a",
          installations: [{ id: "i", agent_id: "a", agent_name: "A", slug: null, installed_at: "2026-10-26T10:00:00.000Z" }],
          last_run: { status: "failed", ran_at: "2026-10-26T11:00:00.000Z", github_url: null, agent_id: "a" },
        },
        { repo: "o/b", installations: [], last_run: null },
      ],
    };
    expect(CiRepoInstallations.parse(body)).toEqual(body);
    expect(CiRepoInstallations.safeParse({ repos: [{ ...body.repos[1], last_run: { status: "bogus", ran_at: null, github_url: null, agent_id: null } }] }).success).toBe(false);
  });
});
