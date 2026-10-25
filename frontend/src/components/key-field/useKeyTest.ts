"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { useTestConnection } from "@/lib/hooks";
import { ApiError } from "@/lib/api";
import type { ConnTestProvider, ConnTestResult } from "@/lib/types";

/**
 * The test-connection action of a key field. `run(key)` posts the typed key (blank → test the
 * stored one) and resolves with the outcome; a transport failure becomes an `ok:false` result.
 * Only one request is in flight at a time (EC17); `result` keeps the last outcome (view-local, D11).
 */
export function useKeyTest(provider: ConnTestProvider) {
  const t = useTranslations("keyField");
  const test = useTestConnection();
  const { mutateAsync } = test;
  const [result, setResult] = React.useState<ConnTestResult | null>(null);
  const inFlight = React.useRef(false);

  const run = React.useCallback(
    async (key: string): Promise<ConnTestResult | null> => {
      if (inFlight.current) return null;
      inFlight.current = true;
      let next: ConnTestResult;
      try {
        next = await mutateAsync({ provider, key: key.trim() || undefined });
      } catch (e) {
        next = { provider, ok: false, message: e instanceof ApiError ? e.message : t("testFailed") };
      } finally {
        inFlight.current = false;
      }
      setResult(next);
      return next;
    },
    [mutateAsync, provider, t],
  );

  return { run, result, pending: test.isPending };
}
