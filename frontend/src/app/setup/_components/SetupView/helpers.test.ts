import { describe, expect, it } from "vitest";
import type { SecretsStatus } from "@/lib/types";
import type { SetupStep } from "./constants";
import { initialProvider, lacksRepoScope, resolveStep, stepsDone, type StepDone } from "./helpers";

const NONE: SecretsStatus = { openai: false, anthropic: false, openrouter: false, github: false };
const ALL: StepDone = { model: true, github: true, repo: true };

describe("resolveStep (the /setup?step= contract, FR22)", () => {
  const cases: { name: string; requested: string | undefined; done: StepDone; want: SetupStep }[] = [
    { name: "model is honoured when nothing is done", requested: "model", done: { model: false, github: false, repo: false }, want: "model" },
    { name: "model is honoured when everything is done (Run setup again, D14)", requested: "model", done: ALL, want: "model" },
    { name: "github is honoured once the model key exists", requested: "github", done: { model: true, github: false, repo: false }, want: "github" },
    { name: "github without a model key falls back to model", requested: "github", done: { model: false, github: false, repo: false }, want: "model" },
    { name: "repo is honoured when the first two are done", requested: "repo", done: { model: true, github: true, repo: false }, want: "repo" },
    { name: "repo without a token falls back to github (EC14)", requested: "repo", done: { model: true, github: false, repo: false }, want: "github" },
    { name: "repo with nothing falls back to model", requested: "repo", done: { model: false, github: false, repo: false }, want: "model" },
    { name: "absent opens the first incomplete step", requested: undefined, done: { model: true, github: false, repo: false }, want: "github" },
    { name: "absent with everything done opens repo", requested: undefined, done: ALL, want: "repo" },
    { name: "unknown opens the first incomplete step", requested: "nope", done: { model: false, github: false, repo: false }, want: "model" },
    { name: "empty opens the first incomplete step", requested: "", done: { model: true, github: true, repo: false }, want: "repo" },
    { name: "a prototype-chain key is unknown", requested: "constructor", done: ALL, want: "repo" },
  ];
  it.each(cases)("$name", ({ requested, done, want }) => {
    expect(resolveStep(requested, done)).toBe(want);
  });
});

describe("stepsDone", () => {
  it("counts any model key, an env-provided token and tested providers, and the repo count", () => {
    expect(stepsDone(NONE, 0, [])).toEqual({ model: false, github: false, repo: false });
    expect(stepsDone({ ...NONE, anthropic: true, github: true }, 2, [])).toEqual(ALL);
    // A passing test stores the key before the secrets-status refetch lands.
    expect(stepsDone(NONE, 0, ["openai", "github"])).toEqual({ model: true, github: true, repo: false });
    // A custom provider completes the model step with no key at all (FR21, D21).
    expect(stepsDone(NONE, 0, [], 1)).toEqual({ model: true, github: false, repo: false });
  });
});

describe("initialProvider and lacksRepoScope", () => {
  it("opens on OpenRouter unless another provider already has a key", () => {
    expect(initialProvider(NONE)).toBe("openrouter");
    expect(initialProvider({ ...NONE, openai: true, anthropic: true })).toBe("openai");
  });

  it("warns only for a classic token (scopes reported) without repo", () => {
    expect(lacksRepoScope(null)).toBe(false);
    expect(lacksRepoScope(undefined)).toBe(false);
    expect(lacksRepoScope([])).toBe(true);
    expect(lacksRepoScope(["gist"])).toBe(true);
    expect(lacksRepoScope(["gist", "repo"])).toBe(false);
  });
});
