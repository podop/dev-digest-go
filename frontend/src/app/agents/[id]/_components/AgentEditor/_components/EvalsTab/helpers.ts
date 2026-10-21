/** The per-agent dashboard page for this agent. */
export function dashboardHref(agentId: string): string {
  return `/eval?agent=${encodeURIComponent(agentId)}`;
}
