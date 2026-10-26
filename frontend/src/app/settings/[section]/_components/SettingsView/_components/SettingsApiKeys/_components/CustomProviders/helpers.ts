/** Who uses a provider, from `error.details` of the 409 `provider_in_use` answer (FR6): agents (id, name), feature ids, default model. */
export interface InUseUsers {
  agents: { id: string; name: string }[];
  features: string[];
  defaultModel: boolean;
}

/** Reads the details defensively; anything unexpected counts as "nobody listed". */
export function inUseUsers(details: unknown): InUseUsers {
  const d = typeof details === "object" && details !== null ? (details as Record<string, unknown>) : {};
  const agents = Array.isArray(d.agents)
    ? d.agents.flatMap((a) => {
        const { id, name } = (a ?? {}) as { id?: unknown; name?: unknown };
        return typeof id === "string" && typeof name === "string" ? [{ id, name }] : [];
      })
    : [];
  const features = Array.isArray(d.feature_models) ? d.feature_models.filter((f): f is string => typeof f === "string") : [];
  return { agents, features, defaultModel: d.default_model === true };
}
