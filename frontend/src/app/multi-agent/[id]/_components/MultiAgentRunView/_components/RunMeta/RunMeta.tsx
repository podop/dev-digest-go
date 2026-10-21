/* RunMeta — "time · cost" of a run or of the whole multi-agent run; unknown parts read "—". */
import React from "react";
import { CostText } from "@/components/cost-text";
import { formatSeconds } from "./helpers";

export function RunMeta({
  durationMs,
  costUsd,
  style,
}: {
  durationMs: number | null | undefined;
  costUsd: number | null | undefined;
  style?: React.CSSProperties;
}) {
  return (
    <span className="mono tnum" style={style}>
      {durationMs == null ? "—" : formatSeconds(durationMs)} · <CostText usd={costUsd} />
    </span>
  );
}
