import type { ConnTestProvider } from "@/lib/types";

/** One configurable provider key row in the API Keys section (FR2). */
export interface KeyRowSpec {
  provider: Exclude<ConnTestProvider, "github">;
  /** Message keys in the `apiKeys` namespace. */
  labelKey: string;
  hintKey: string;
  /** Shows the "Optional" tag next to the label. */
  optional?: boolean;
}

/** The provider key rows in display order: OpenRouter first, then OpenAI, Anthropic. GitHub lives in GitHub Integration. */
export const KEY_ROWS: readonly KeyRowSpec[] = [
  { provider: "openrouter", labelKey: "openrouterLabel", hintKey: "openrouterHint" },
  { provider: "openai", labelKey: "openaiLabel", hintKey: "openaiHint", optional: true },
  { provider: "anthropic", labelKey: "anthropicLabel", hintKey: "anthropicHint" },
];
