import { describe, it, expect } from "vitest";
import {
  buildCreate,
  buildUpdate,
  emptyForm,
  fieldErrorsFrom,
  formFromProvider,
  keepsStoredValue,
  localProblems,
  newHeader,
  withName,
  withProtocol,
} from "./helpers";
import type { CustomProvider } from "@/lib/types";

const STORED: CustomProvider = {
  id: "custom:synapse",
  name: "Synapse",
  protocol: "openai",
  base_url: "https://gw.example.com/v1",
  plain_http: false,
  auth_mode: "bearer",
  auth_header: null,
  structured_output: "json_object",
  key_configured: true,
  extra_headers: [{ name: "X-Tenant", value_configured: true }],
  models: [],
  created_at: "2026-10-26T00:00:00Z",
  updated_at: "2026-10-26T00:00:00Z",
};

describe("custom provider form", () => {
  it("follows the name with the slug until the slug is edited (FR17)", () => {
    const f = withName(emptyForm(), "Synapse Prod");
    expect(f.slug).toBe("synapse-prod");
    expect(withName({ ...f, slug: "mine", slugTouched: true }, "Other").slug).toBe("mine");
  });

  it("switches auth defaults with the protocol until the user chose one, and resets json_object (FR4, FR34)", () => {
    const toAnthropic = withProtocol({ ...emptyForm(), structured: "json_object" }, "anthropic");
    expect(toAnthropic.modeReset).toBe(true);
    expect(toAnthropic.form).toMatchObject({ protocol: "anthropic", authMode: "header", authHeader: "x-api-key", structured: "strict" });

    const chosen = withProtocol({ ...emptyForm(), authTouched: true, authMode: "bearer" }, "anthropic");
    expect(chosen.form.authMode).toBe("bearer");
    expect(chosen.modeReset).toBe(false);
    expect(withProtocol(emptyForm(), "openai").modeReset).toBe(false);
  });

  it("sends secrets only when typed: no api_key and no header value for untouched stored ones (FR5)", () => {
    const f = formFromProvider(STORED);
    expect(keepsStoredValue(f.headers[0]!)).toBe(true);
    expect(buildUpdate(f, true)).toEqual({
      name: "Synapse",
      protocol: "openai",
      base_url: "https://gw.example.com/v1",
      auth_mode: "bearer",
      auth_header: null,
      structured_output: "json_object",
      extra_headers: [{ name: "X-Tenant" }],
      models: [],
    });

    const typed = { ...f, apiKey: " sk-new ", headers: [{ ...f.headers[0]!, value: "t-2" }] };
    expect(buildUpdate(typed, true)).toMatchObject({ api_key: "sk-new", extra_headers: [{ name: "X-Tenant", value: "t-2" }] });
    expect(buildUpdate({ ...f, removeKey: true }, true)).toMatchObject({ api_key: null });
    expect(buildUpdate({ ...f, removeKey: true, apiKey: "k" }, true)).toMatchObject({ api_key: "k" });
    expect("api_key" in buildUpdate({ ...emptyForm(), removeKey: true }, false)).toBe(false);
  });

  it("a renamed stored header no longer keeps its value", () => {
    const h = formFromProvider(STORED).headers[0]!;
    expect(keepsStoredValue({ ...h, name: "x-tenant" })).toBe(true);
    expect(keepsStoredValue({ ...h, name: "X-Other" })).toBe(false);
    expect(keepsStoredValue(newHeader())).toBe(false);
  });

  it("builds the create body with the custom id and reports what blocks Save", () => {
    const f = { ...withName(emptyForm(), "My GW"), baseUrl: " https://gw/v1 " };
    expect(buildCreate(f)).toMatchObject({ id: "custom:my-gw", name: "My GW", base_url: "https://gw/v1" });
    expect([...localProblems(emptyForm())].sort()).toEqual(["baseUrl", "name", "slug"]);
    expect([...localProblems({ ...f, authMode: "header", authHeader: " " })]).toEqual(["authHeader"]);
    expect([...localProblems({ ...f, headers: [newHeader()] })]).toEqual(["headers"]);
    expect([...localProblems(f)]).toEqual([]);
  });

  it("maps the server's 422 details to field paths and keeps unknown ones apart", () => {
    const server = (path: (string | number)[], message: string, instancePath = `/${path.join("/")}`) => ({
      keyword: "custom",
      instancePath,
      schemaPath: `#${instancePath}/custom`,
      message,
      params: { issue: { code: "custom", path, message } },
    });
    const out = fieldErrorsFrom([
      server(["base_url"], "Base URL resolves to a forbidden address"),
      server(["extra_headers", 1, "value"], "Too long"),
      server(["base_url"], "second message"),
      server([], "Body is not an object", "/"),
    ]);
    expect(out.fields).toEqual({ base_url: "Base URL resolves to a forbidden address", "extra_headers.1.value": "Too long" });
    expect(out.rest).toEqual(["Body is not an object"]);
    expect(fieldErrorsFrom(undefined)).toEqual({ fields: {}, rest: [] });
  });

  it("falls back to instancePath, then to a Zod-style path, and to params.issue.message", () => {
    const out = fieldErrorsFrom([
      { instancePath: "/models/0/pricing/promptPerM", message: "From instancePath" },
      { path: ["name"], message: "Zod-style" },
      { instancePath: "/auth_header", params: { issue: { message: "Nested message" } } },
    ]);
    expect(out.fields).toEqual({
      "models.0.pricing.promptPerM": "From instancePath",
      name: "Zod-style",
      auth_header: "Nested message",
    });
  });
});
