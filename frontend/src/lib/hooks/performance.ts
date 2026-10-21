/* hooks/performance.ts — React Query hooks of the Agent Performance dashboard and the agent
   Stats tab (specs/2026-10-16-agent-performance.md §7). Read-only GETs: the query key carries
   the URL period, the ISO instants are built in the queryFn, so a period change is a new key
   (no placeholder data — the old period's numbers are never shown) and nothing polls. */
"use client";

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import type { AgentPerf, AgentPeriodMetrics, AgentStats } from "@devdigest/shared";
import { api } from "../api";
import { periodApiQuery, periodSearch, type StatsPeriod } from "../stats-period";
import { performanceKeys } from "./keys";

/** GET /agents/performance — one row per agent for the period. */
export function useAgentPerformance(period: StatsPeriod) {
  return useQuery({
    queryKey: performanceKeys.dashboard(periodSearch(period)),
    queryFn: () => api.get<AgentPerf>(`/agents/performance?${periodApiQuery(period)}`),
  });
}

/** GET /agents/:id/stats — the same aggregation for one agent. */
export function useAgentStats(agentId: string | null | undefined, period: StatsPeriod) {
  return useQuery({
    queryKey: performanceKeys.agentStats(agentId, periodSearch(period)),
    queryFn: () => api.get<AgentStats>(`/agents/${encodeURIComponent(agentId ?? "")}/stats?${periodApiQuery(period)}`),
    enabled: !!agentId,
  });
}

/** The list cards always show the last 30 days. */
const CARD_PERIOD: StatsPeriod = { key: "30d" };

/** Last-30-days metrics per agent id for the agent list cards (same query as the 30d dashboard); empty while loading. */
export function useAgentCardMetrics(): ReadonlyMap<string, AgentPeriodMetrics> {
  const { data } = useAgentPerformance(CARD_PERIOD);
  return useMemo(() => {
    const byId = new Map<string, AgentPeriodMetrics>();
    for (const row of data?.agents ?? []) if (row.agent_id !== null) byId.set(row.agent_id, row.metrics);
    return byId;
  }, [data]);
}
