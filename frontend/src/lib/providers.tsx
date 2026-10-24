/* providers.tsx — client provider stack: React Query + Theme + active Repo. */
"use client";

import React from "react";
import {
  QueryClient,
  QueryClientProvider,
  QueryCache,
  MutationCache,
} from "@tanstack/react-query";
import { ThemeProvider } from "./theme";
import { RepoProvider } from "./repo-context";
import { ToastProvider, notify } from "./toast";
import { LeaveGuardProvider } from "./leave-guard";
import { ApiError } from "./api";
import { reportMutationError } from "./mutation-errors";

function errorMessage(e: unknown): string {
  if (e instanceof Error) return e.message;
  return "Something went wrong";
}

/** The app's QueryClient with the global error surfacing (exported for tests). */
export function createAppQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        retry: 1,
        staleTime: 30_000,
        refetchOnWindowFocus: false,
      },
    },
    // Global error surfacing (errors anywhere → toast). Mutations always
    // toast (they are user actions). Queries only toast on network/5xx —
    // expected 4xx like a 404 "no tour yet" stay silent for inline empty states.
    queryCache: new QueryCache({
      onError: (err) => {
        const status = err instanceof ApiError ? err.status : 500;
        if (status === 0 || status >= 500) notify.error(errorMessage(err));
      },
    }),
    // A mutation may surface some errors itself (meta.quietErrorCodes); a 402
    // spend-cap refusal is always toasted (mutation-errors.tsx).
    mutationCache: new MutationCache({
      onError: (err, _vars, _ctx, mutation) => reportMutationError(err, mutation.meta),
    }),
  });
}

export function Providers({ children }: { children: React.ReactNode }) {
  const [qc] = React.useState(createAppQueryClient);
  return (
    <QueryClientProvider client={qc}>
      <ThemeProvider>
        <ToastProvider>
          <RepoProvider>
            <LeaveGuardProvider>{children}</LeaveGuardProvider>
          </RepoProvider>
        </ToastProvider>
      </ThemeProvider>
    </QueryClientProvider>
  );
}
