/* Mirrors CUSTOM_PROVIDER_LIMITS of the shared contract (vendor/shared/contracts/platform.ts). Kept local so the
   Settings route does not pull zod in for four numbers. */
export const MAX_MODELS = 200;
export const MODEL_ID_MAX = 200;
export const MODEL_LABEL_MAX = 200;
export const PRICE_MAX = 10_000;
