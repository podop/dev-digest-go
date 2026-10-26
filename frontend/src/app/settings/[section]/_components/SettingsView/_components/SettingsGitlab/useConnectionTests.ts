/* useConnectionTests — the view-local connection test results of Settings → GitLab Integration (FR33, FR34, D11).
   `run(id)` tests one connection's stored token; on the first load of the list the first 5 connections with a
   token are tested one after another (D28). A transport failure becomes an `ok:false` result. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { ApiError } from "@/lib/api";
import { useTestConnection } from "@/lib/hooks";
import type { ConnTestResult, GitLabConnection } from "@/lib/types";
import { autoTestIds } from "./helpers";

export interface TestEntry {
  pending: boolean;
  result: ConnTestResult | null;
}

export function useConnectionTests(list: readonly GitLabConnection[] | undefined) {
  const t = useTranslations("gitlabIntegration.result");
  const { mutateAsync } = useTestConnection();
  const [entries, setEntries] = React.useState<Readonly<Record<string, TestEntry>>>({});
  const inFlight = React.useRef(new Set<string>());
  const mounted = React.useRef(true);
  const autoStarted = React.useRef(false);

  React.useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  /** Keep a result that was produced elsewhere (the dialog's "Save and test"). */
  const record = React.useCallback((id: string, result: ConnTestResult) => {
    setEntries((e) => ({ ...e, [id]: { pending: false, result } }));
  }, []);

  const run = React.useCallback(
    async (id: string) => {
      if (inFlight.current.has(id)) return;
      inFlight.current.add(id);
      setEntries((e) => ({ ...e, [id]: { pending: true, result: null } }));
      let result: ConnTestResult;
      try {
        result = await mutateAsync({ provider: "gitlab", connection_id: id });
      } catch (e) {
        result = { provider: "gitlab", ok: false, message: e instanceof ApiError ? e.message : t("failed") };
      } finally {
        inFlight.current.delete(id);
      }
      if (mounted.current) setEntries((e) => ({ ...e, [id]: { pending: false, result } }));
    },
    [mutateAsync, t],
  );

  React.useEffect(() => {
    if (!list || autoStarted.current) return;
    autoStarted.current = true;
    const ids = autoTestIds(list);
    void (async () => {
      for (const id of ids) {
        if (!mounted.current) return;
        await run(id);
      }
    })();
  }, [list, run]);

  return { entries, run, record };
}
