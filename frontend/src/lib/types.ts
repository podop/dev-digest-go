/**
 * Shared contract types re-exported from @devdigest/shared (single source of
 * truth). F2 imports these rather than redefining them.
 *
 * F1 (@devdigest/shared) currently exports all the platform/findings/brief/
 * knowledge/trace contracts we need for the scaffolding screens, so there are
 * NO local placeholders required at this time. If a feature agent's contract is
 * not yet exported, add a placeholder below marked
 * `// TODO: reconcile with @devdigest/shared`.
 */
export type {
  Settings,
  SettingsUpdate,
  ConnTestProvider,
  ConnTestResult,
  SecretsStatus,
  SpendStatus,
  SpendCapDetails,
  AutomaticReviews,
  AutomaticReviewsInput,
  AutomaticReviewsStatus,
  About,
  FeatureModelId,
  FeatureModelChoice,
  FeatureModelDef,
  Provider,
  CustomProviderId,
  ModelInfo,
  CustomProvider,
  CustomProviderInput,
  CustomProviderUpdate,
  CustomProviderModel,
  CustomProviderModelInput,
  CustomProviderHeader,
  CustomProviderHeaderInput,
  CustomProviderProtocol,
  CustomProviderAuthMode,
  CustomProviderStructuredOutput,
  CustomProviderDeleted,
  CustomProviderInUseDetails,
  ModelPricing,
  Repo,
  RepoInput,
  PrMeta,
  PrDetail,
  PrFile,
  PrCommit,
  PrReviewComment,
  PrStatus,
  RetentionDays,
  WorkspaceInfo,
  WorkspaceRepo,
  WorkspaceStats,
  WorkspaceResetDeleted,
  WorkspaceResetResult,
} from "@devdigest/shared";

export type { Review, Finding, Severity, Verdict } from "@devdigest/shared";
export type { PrBrief, SmartDiff } from "@devdigest/shared";


export type {
  PluginRef,
  PluginCounts,
  PluginExportRequest,
  PluginImportPreview,
  PluginImportResult,
  PluginConflictDetails,
  PluginInvalidDetails,
  PluginList,
  PluginLastExport,
  PluginEnabledRequest,
  PluginRemoveRequest,
  PluginRemoveResult,
  InstalledPlugin,
  PluginRow,
  CiRepoEntry,
  CiRepoInstallation,
  CiRepoInstallations,
  CiRepoLastRun,
} from "@devdigest/shared";
