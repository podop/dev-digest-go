/* useUploadStoreFiles — creates the picked markdown files in the store one by one
   (a taken name is suffixed by the server). Files that cannot be stored are skipped
   with a toast naming them; progress is `current/total`. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { PROJECT_CONTEXT_STORE_ROOT } from "@devdigest/shared/constants/project-context";
import type { ContextFileCreateInput } from "@devdigest/shared";
import { useToast } from "@/lib/toast";
import { classifyUpload } from "../../helpers";

export interface UploadProgress {
  current: number;
  total: number;
}

export function useUploadStoreFiles(createFile: (input: ContextFileCreateInput) => Promise<unknown>) {
  const t = useTranslations("context");
  const toast = useToast();
  const [progress, setProgress] = React.useState<UploadProgress | null>(null);

  const upload = async (picked: readonly File[]) => {
    const total = picked.length;
    let created = 0;
    try {
      for (const [i, file] of picked.entries()) {
        setProgress({ current: i + 1, total });
        const verdict = await classifyUpload(file);
        if (!verdict.ok) {
          toast.error(t(`store.uploadStatus.skipped.${verdict.reason}`, { name: file.name }));
          continue;
        }
        try {
          await createFile({ path: PROJECT_CONTEXT_STORE_ROOT + file.name, content: verdict.content, on_conflict: "suffix" });
          created += 1;
        } catch {
          break; // the failure was already toasted by the global mutation handler
        }
      }
    } finally {
      setProgress(null);
    }
    if (created > 0) toast.success(t("store.uploadStatus.done", { count: created }));
  };

  return { upload, progress };
}
