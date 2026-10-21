/* usePrDetailSearch — the screen's URL-backed view state (?tab, ?trace, ?order,
   and the Files changed focus ?file/?line from a finding's file:line link). */
"use client";

import { useRouter, useSearchParams } from "next/navigation";
import { DIFF_FILE_PARAM, DIFF_LINE_PARAM, parseDiffFocus } from "@/lib/pr-urls";
import { ORDER_PARAM, TAB_PARAM, TRACE_PARAM, type DiffOrder } from "./constants";
import { parseOrder, parseTab, prDetailPath, withSearchParam } from "./helpers";

export function usePrDetailSearch(repoId: string, number: string) {
  const search = useSearchParams();
  const router = useRouter();
  const replace = (qs: string) => router.replace(`${prDetailPath(repoId, number)}${qs}`);
  const setParam = (key: string, value: string | null) => replace(withSearchParam(search.toString(), key, value));

  return {
    tab: parseTab(search.get(TAB_PARAM)),
    traceRunId: search.get(TRACE_PARAM),
    order: parseOrder(search.get(ORDER_PARAM)),
    diffFocus: parseDiffFocus(search.get(DIFF_FILE_PARAM), search.get(DIFF_LINE_PARAM)),
    // Switching tabs drops the diff focus, so coming back to Files changed doesn't re-jump.
    setTab: (tab: string) => {
      const qs = withSearchParam(withSearchParam(search.toString(), DIFF_FILE_PARAM, null), DIFF_LINE_PARAM, null);
      replace(withSearchParam(qs, TAB_PARAM, tab));
    },
    openTrace: (runId: string) => setParam(TRACE_PARAM, runId),
    closeTrace: () => setParam(TRACE_PARAM, null),
    setOrder: (order: DiffOrder) => setParam(ORDER_PARAM, order),
  };
}
