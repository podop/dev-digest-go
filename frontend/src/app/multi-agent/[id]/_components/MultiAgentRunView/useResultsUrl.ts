/* useResultsUrl — the results page's URL-backed state (?view, ?trace): changes go through router.replace. */
"use client";

import { useRouter } from "next/navigation";
import type { ResultsView } from "./constants";
import { resultsPath } from "./helpers";

export function useResultsUrl(id: string, view: ResultsView, traceRunId: string | null) {
  const router = useRouter();
  return {
    setView: (next: ResultsView) => router.replace(resultsPath(id, { view: next, traceRunId })),
    openTrace: (runId: string) => router.replace(resultsPath(id, { view, traceRunId: runId })),
    closeTrace: () => router.replace(resultsPath(id, { view, traceRunId: null })),
  };
}
