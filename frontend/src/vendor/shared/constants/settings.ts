/**
 * Zod-free Settings constants, re-exported by contracts/platform. Import this subpath
 * (`@devdigest/shared/constants/settings`) from client bundles that must not pull zod in.
 */

/** Monthly spend cap bounds (USD): 0..1,000,000 with at most two decimals. */
export const SPEND_CAP_MIN_USD = 0;
export const SPEND_CAP_MAX_USD = 1_000_000;
export const SPEND_CAP_MAX_DECIMALS = 2;

/** Error code of the 402 answered by the LLM entry routes once the cap is reached. */
export const SPEND_CAP_ERROR_CODE = 'spend_cap_reached';

/** Runner kind reported by GET /about. */
export const ABOUT_RUNNER_KIND = 'go' as const;
