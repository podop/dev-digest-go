/* useInstallRun — installs the agents in a repo one at a time (FR3, EC13): each POST waits for
   the previous one, a failure is recorded for that agent and the loop goes on, and `run` can be
   called again with just the failed ids (Retry failed). Every request carries the dialog's Runner (FR32). */
"use client";

import React from "react";
import { ApiError } from "@/lib/api";
import { safeRunHref } from "@/lib/ci-runs";
import { useInstallCi } from "@/lib/hooks";
import { gitlabNextSteps, type AgentRun, type RepoRef, type Runner, type RunMap } from "./helpers";

export function useInstallRun(repoRef: RepoRef, runner: Runner) {
  const { mutateAsync } = useInstallCi();
  const [runs, setRuns] = React.useState<RunMap>({});
  const [busy, setBusy] = React.useState(false);
  const busyRef = React.useRef(false);
  const aliveRef = React.useRef(true);

  React.useEffect(() => {
    aliveRef.current = true;
    return () => {
      aliveRef.current = false;
    };
  }, []);

  const mark = React.useCallback((id: string, value: AgentRun) => {
    if (aliveRef.current) setRuns((prev) => ({ ...prev, [id]: value }));
  }, []);

  const run = React.useCallback(
    async (ids: readonly string[]) => {
      if (busyRef.current || ids.length === 0) return;
      busyRef.current = true;
      setBusy(true);
      for (const id of ids) mark(id, { status: "queued" });
      for (const id of ids) {
        if (!aliveRef.current) break;
        mark(id, { status: "running" });
        try {
          const result = await mutateAsync({ agentId: id, ...repoRef, runner });
          // The response is not zod-parsed here: a missing list means none.
          mark(id, {
            status: "done",
            prUrl: safeRunHref(result.pr_url),
            secrets: result.required_secrets ?? [],
            variables: result.required_variables ?? [],
            gitlab: gitlabNextSteps(result),
          });
        } catch (e) {
          // The failure belongs to this agent; the next one is still tried (EC13).
          mark(id, { status: "failed", message: e instanceof ApiError ? e.message : null });
        }
      }
      busyRef.current = false;
      if (aliveRef.current) setBusy(false);
    },
    [mutateAsync, repoRef, runner, mark],
  );

  return { runs, busy, run };
}
