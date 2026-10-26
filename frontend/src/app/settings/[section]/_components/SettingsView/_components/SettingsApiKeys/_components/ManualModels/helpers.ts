import type { CustomProviderModel, CustomProviderModelInput } from "@/lib/types";
import { PRICE_MAX } from "./constants";

/** One editable manual-model row; prices stay text until they are validated and sent (FR18). */
export interface ModelDraft {
  /** Stable React key; not sent. */
  key: string;
  id: string;
  label: string;
  prompt: string;
  completion: string;
}

/** Messages-key suffix of a row problem, under `customProviders.models`. */
export type ModelProblem = "idRequired" | "idDuplicate" | "priceInvalid" | "priceBoth";

export interface ModelRowProblems {
  id?: ModelProblem;
  prompt?: ModelProblem;
  completion?: ModelProblem;
}

let seq = 0;
/** A fresh row key. */
export function nextKey(prefix: string): string {
  seq += 1;
  return `${prefix}-${seq}`;
}

export function newModelDraft(): ModelDraft {
  return { key: nextKey("model"), id: "", label: "", prompt: "", completion: "" };
}

const PRICE = /^\d+(\.\d{1,6})?$/;

/** A price per 1M tokens: a plain decimal from 0 to 10,000 with at most 6 decimals (FR1). */
export function isValidPrice(text: string): boolean {
  const v = text.trim();
  return PRICE.test(v) && Number(v) <= PRICE_MAX;
}

/** Problems of every row, in row order. A row with only one price filled is incomplete (pricing is both-or-none). */
export function modelProblems(drafts: readonly ModelDraft[]): ModelRowProblems[] {
  const seen = new Set<string>();
  return drafts.map((d) => {
    const out: ModelRowProblems = {};
    const id = d.id.trim();
    if (id === "") out.id = "idRequired";
    else if (seen.has(id)) out.id = "idDuplicate";
    seen.add(id);

    const prompt = d.prompt.trim();
    const completion = d.completion.trim();
    if (prompt !== "" && !isValidPrice(prompt)) out.prompt = "priceInvalid";
    if (completion !== "" && !isValidPrice(completion)) out.completion = "priceInvalid";
    if (prompt !== "" && completion === "") out.completion ??= "priceBoth";
    if (completion !== "" && prompt === "") out.prompt ??= "priceBoth";
    return out;
  });
}

/** Whether any row has a problem. */
export function hasModelProblems(drafts: readonly ModelDraft[]): boolean {
  return modelProblems(drafts).some((p) => p.id || p.prompt || p.completion);
}

export function draftsFromModels(models: readonly CustomProviderModel[]): ModelDraft[] {
  return models.map((m) => ({
    key: nextKey("model"),
    id: m.id,
    label: m.label ?? "",
    prompt: m.pricing ? String(m.pricing.promptPerM) : "",
    completion: m.pricing ? String(m.pricing.completionPerM) : "",
  }));
}

/** The request list: trimmed ids, a label only when typed, `pricing` only when both prices are given. */
export function modelsToInput(drafts: readonly ModelDraft[]): CustomProviderModelInput[] {
  return drafts.map((d) => {
    const label = d.label.trim();
    const hasPrice = d.prompt.trim() !== "" && d.completion.trim() !== "";
    return {
      id: d.id.trim(),
      label: label === "" ? null : label,
      pricing: hasPrice ? { promptPerM: Number(d.prompt.trim()), completionPerM: Number(d.completion.trim()) } : null,
    };
  });
}
