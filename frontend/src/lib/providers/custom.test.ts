import { describe, it, expect } from "vitest";
import { CustomProvider, CustomProviderInput, CustomProviderUpdate } from "@devdigest/shared/contracts/platform";
import { Provider } from "@devdigest/shared/contracts/knowledge";
import { ModelInfo } from "@devdigest/shared/adapters";
import { AgentManifest, CiExport, CiExportInput } from "@devdigest/shared/contracts/eval-ci";
import { PluginAgent } from "@devdigest/shared/contracts/productionize";
import {
  hostOf,
  isCustomId,
  isUndefinedCustomId,
  providerName,
  providerOptions,
  slugFromName,
} from "./custom";

const lab = { id: "custom:lab", name: "Lab" };
const synapse = { id: "custom:synapse", name: "Synapse" };

describe("provider ids", () => {
  it("isCustomId accepts only well-formed custom ids", () => {
    expect(isCustomId("custom:synapse")).toBe(true);
    expect(isCustomId("custom:a-1")).toBe(true);
    for (const bad of ["openai", "custom:", "custom:Synapse", "custom:a_b", `custom:${"a".repeat(33)}`, "x custom:a", "", null, undefined]) {
      expect(isCustomId(bad)).toBe(false);
    }
  });

  it("the shared Provider contract agrees with isCustomId", () => {
    for (const ok of ["openai", "anthropic", "openrouter", "custom:synapse"]) expect(Provider.safeParse(ok).success).toBe(true);
    for (const bad of ["github", "custom:", "custom:UP", "custom:a b"]) expect(Provider.safeParse(bad).success).toBe(false);
  });
});

describe("providerOptions", () => {
  it("lists OpenRouter, OpenAI, Anthropic, then custom providers by name", () => {
    const opts = providerOptions([synapse, lab]);
    expect(opts.map((o) => [o.value, o.label, o.custom])).toEqual([
      ["openrouter", "OpenRouter", false],
      ["openai", "OpenAI", false],
      ["anthropic", "Anthropic", false],
      ["custom:lab", "Lab", true],
      ["custom:synapse", "Synapse", true],
    ]);
  });

  it("breaks name ties by id, does not mutate the input and tolerates no list", () => {
    const input = [
      { id: "custom:b", name: "Same" },
      { id: "custom:a", name: "Same" },
    ];
    expect(providerOptions(input).slice(3).map((o) => o.value)).toEqual(["custom:a", "custom:b"]);
    expect(input[0]?.id).toBe("custom:b");
    expect(providerOptions(undefined)).toHaveLength(3);
    expect(providerOptions(null, { openai: "Open AI" })[1]?.label).toBe("Open AI");
  });
});

describe("providerName", () => {
  it("shows built-in names, a custom provider's current name, else the raw id", () => {
    expect(providerName("openrouter", [])).toBe("OpenRouter");
    expect(providerName("custom:synapse", [synapse])).toBe("Synapse");
    expect(providerName("custom:synapse", [{ id: "custom:synapse", name: "Synapse EU" }])).toBe("Synapse EU");
    expect(providerName("custom:ghost", [synapse])).toBe("custom:ghost");
    expect(providerName("custom:ghost", undefined)).toBe("custom:ghost");
    expect(providerName(null, [synapse])).toBe("");
  });

  it("isUndefinedCustomId is true only for a well-formed custom id nobody defines", () => {
    expect(isUndefinedCustomId("custom:ghost", [synapse])).toBe(true);
    expect(isUndefinedCustomId("custom:synapse", [synapse])).toBe(false);
    expect(isUndefinedCustomId("openai", [])).toBe(false);
  });
});

describe("slugFromName", () => {
  it("lower-cases, turns runs of other characters into one dash, trims and cuts to 32", () => {
    expect(slugFromName("  My Gateway (EU)!  ")).toBe("my-gateway-eu");
    expect(slugFromName("Synapse")).toBe("synapse");
    expect(slugFromName("Ünï cödé")).toBe("n-c-d");
    expect(slugFromName("---")).toBe("");
    const long = slugFromName(`${"a".repeat(31)} bbb`);
    expect(long).toBe(`${"a".repeat(31)}`);
    expect(slugFromName("x".repeat(40))).toHaveLength(32);
  });
});

describe("hostOf", () => {
  it("returns the host with a non-default port and falls back to the input", () => {
    expect(hostOf("https://gateway.example.com/v1")).toBe("gateway.example.com");
    expect(hostOf("http://localhost:8080/v1")).toBe("localhost:8080");
    expect(hostOf("https://[::1]:8443/v1")).toBe("[::1]:8443");
    expect(hostOf("not a url")).toBe("not a url");
  });
});

describe("shared contracts of custom providers", () => {
  const response = {
    id: "custom:synapse",
    name: "Synapse",
    protocol: "openai",
    base_url: "https://gw.example.com/v1",
    plain_http: false,
    auth_mode: "bearer",
    auth_header: null,
    structured_output: "strict",
    key_configured: true,
    extra_headers: [{ name: "X-Team", value_configured: true }],
    models: [{ id: "m1", label: null, pricing: { promptPerM: 2, completionPerM: 8 } }],
    created_at: "2026-10-26T10:00:00.000Z",
    updated_at: "2026-10-26T10:00:00.000Z",
  };

  it("a response parses and carries no secret fields", () => {
    const parsed = CustomProvider.parse(response);
    expect(Object.keys(parsed)).not.toContain("api_key");
    expect(parsed.extra_headers[0]).toEqual({ name: "X-Team", value_configured: true });
  });

  it("a request body parses with defaults omitted; PUT is the same without id; limits are enforced", () => {
    const body = { id: "custom:synapse", name: " Synapse ", protocol: "anthropic", base_url: "https://gw.example.com/v1" };
    expect(CustomProviderInput.parse(body).name).toBe("Synapse");
    expect(CustomProviderUpdate.safeParse({ ...body, id: undefined }).success).toBe(true);
    expect(CustomProviderInput.safeParse({ ...body, api_key: null, extra_headers: [{ name: "X" }] }).success).toBe(true);
    expect(CustomProviderInput.safeParse({ ...body, api_key: "" }).success).toBe(false);
    expect(CustomProviderInput.safeParse({ ...body, id: "synapse" }).success).toBe(false);
    expect(CustomProviderInput.safeParse({ ...body, extra_headers: Array.from({ length: 6 }, (_, i) => ({ name: `H${i}` })) }).success).toBe(false);
    expect(CustomProviderInput.safeParse({ ...body, models: [{ id: "m", pricing: { promptPerM: 10_001, completionPerM: 0 } }] }).success).toBe(false);
  });

  it("every place that carries a provider accepts a custom id", () => {
    expect(ModelInfo.parse({ id: "m", provider: "custom:synapse" }).provider).toBe("custom:synapse");
    expect(ModelInfo.safeParse({ id: "m", provider: "custom:" }).success).toBe(false);
    const agent = { name: "a", description: "", provider: "custom:synapse", model: "m", system_prompt: "", enabled: true, skills: [] };
    expect(PluginAgent.safeParse(agent).success).toBe(true);
    expect(PluginAgent.safeParse({ ...agent, provider: "gemini" }).success).toBe(false);
  });

  it("the CI manifest and export carry the custom provider, pricing and the secret and variable lists", () => {
    const manifest = AgentManifest.parse({
      name: "a",
      provider: "custom:synapse",
      model: "m",
      system_prompt: "p",
      custom_provider: {
        protocol: "openai",
        auth_mode: "bearer",
        auth_header: null,
        extra_headers: ["X-Team"],
        structured_output: "strict",
        pricing: null,
      },
    });
    expect(manifest.custom_provider?.extra_headers).toEqual(["X-Team"]);
    expect(AgentManifest.parse({ name: "a", model: "m", system_prompt: "p" }).provider).toBe("openrouter");
    expect(AgentManifest.parse({ name: "a", provider: "openai", model: "m", system_prompt: "p", pricing: { promptPerM: 1, completionPerM: 2 } }).pricing).toEqual({ promptPerM: 1, completionPerM: 2 });
    expect(CiExportInput.parse({ repo: "o/a" }).runner).toBe("github_hosted");
    expect(CiExportInput.parse({ repo: "o/a", runner: "self_hosted" }).runner).toBe("self_hosted");
    const out = CiExport.parse({ installation: null, files: [], pr_url: null, required_secrets: ["DEVDIGEST_SYNAPSE_API_KEY"], required_variables: ["DEVDIGEST_SYNAPSE_BASE_URL"] });
    expect(out.required_secrets).toEqual(["DEVDIGEST_SYNAPSE_API_KEY"]);
    expect(out.required_variables).toEqual(["DEVDIGEST_SYNAPSE_BASE_URL"]);
  });
});
