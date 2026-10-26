/* useProviderUndefined — whether a provider id is a custom id that no provider defines ("Not configured",
   FR19/EC23). Unknown while the list loads or when it failed, so then it is false: saving is not blocked on a
   guess, and the server refuses an unknown custom id anyway (422). */
"use client";

import { useCustomProviders } from "@/lib/hooks";
import { isUndefinedCustomId } from "@/lib/providers/custom";

export function useProviderUndefined(provider: string | null | undefined): boolean {
  const { data } = useCustomProviders();
  return data !== undefined && isUndefinedCustomId(provider, data);
}
