/**
 * Zod-free Onboarding Tour limits and section ids, re-exported by
 * contracts/onboarding. Import this subpath (`@devdigest/shared/constants/onboarding`)
 * from client bundles that must not pull zod in.
 */

/** The five tour sections, in display order. Used as TOC entries and `#` anchors. */
export const ONBOARDING_SECTION_IDS = [
  'architecture',
  'critical-paths',
  'run-locally',
  'reading-path',
  'first-tasks',
] as const;
export type OnboardingSectionId = (typeof ONBOARDING_SECTION_IDS)[number];

export const ONBOARDING_NODE_KINDS = ['entry', 'module', 'store', 'external'] as const;
export const ONBOARDING_COMPLEXITIES = ['low', 'medium', 'high'] as const;
export const ONBOARDING_STALE_REASONS = ['index_changed', 'prompt_changed'] as const;

/** Output language of the tour; pinned in the prompt. */
export const ONBOARDING_LANGUAGE = 'en' as const;

/** FR4 limits. Minimums apply at normalisation: a section below its minimum is stored empty. */
export const ONBOARDING_LIMITS = {
  summaryMax: 800,
  nodesMin: 2,
  nodesMax: 12,
  edgesMax: 20,
  nodeIdMax: 64,
  nodeLabelMax: 60,
  criticalPathsMin: 3,
  criticalPathsMax: 6,
  readingPathMin: 3,
  readingPathMax: 7,
  runStepsMin: 1,
  runStepsMax: 8,
  commandMax: 200,
  commentMax: 120,
  reasonMax: 120,
  firstTasksMax: 3,
  taskTitleMax: 80,
  pathMax: 512,
} as const;
