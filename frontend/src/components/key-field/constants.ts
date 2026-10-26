import type { ConnTestProvider } from "@/lib/types";

/** Longest run of mask characters shown for a hidden key (FR3). */
export const MASK_MAX_DOTS = 28;
/** Trailing characters of a hidden key that stay readable (FR3). */
export const MASK_TAIL = 4;
export const MASK_CHAR = "•";

/** Where each provider's key / token is created (A10). Opened in a new tab. GitLab has none: its token page is per instance. */
export const KEY_PAGE_URLS = {
  openrouter: "https://openrouter.ai/keys",
  openai: "https://platform.openai.com/api-keys",
  anthropic: "https://console.anthropic.com/settings/keys",
  github: "https://github.com/settings/tokens",
} as const satisfies Record<Exclude<ConnTestProvider, "gitlab">, string>;
