"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import { prDiffHref } from "@/lib/pr-urls";
import type { FileRef } from "./helpers";

/** Opens a PR file in Files changed (a new history entry), or remembers which item pointed at
 *  a file outside the PR's diff so the list can say so inline. Only a changed path with a
 *  parsed line is ever navigated to. */
export function useOpenInDiff({ repoId, number, changedPaths }: { repoId: string; number: string; changedPaths: readonly string[] }) {
  const router = useRouter();
  const [missingKey, setMissingKey] = useState<string | null>(null);

  function open(key: string, target: FileRef) {
    if (!changedPaths.includes(target.file)) {
      setMissingKey(key);
      return;
    }
    setMissingKey(null);
    router.push(prDiffHref(repoId, number, target));
  }

  return { open, missingKey };
}

/** What a list needs to open a file reference. */
export interface OpenInDiff {
  open: (key: string, target: FileRef) => void;
  missingKey: string | null;
}
