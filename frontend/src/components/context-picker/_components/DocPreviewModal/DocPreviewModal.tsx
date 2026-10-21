/* DocPreviewModal — a document's rendered markdown over the Context tab
   (FR16). Raw HTML in a document is printed as text by the vendored
   <Markdown>, never executed. Rendered by the picker as a sibling of the rows. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Badge, ErrorState, Markdown, Modal, Skeleton } from "@devdigest/ui";
import { ApiError } from "@/lib/api";
import { useContextDoc } from "@/lib/hooks";
import { DOC_TYPE_STYLE } from "../../constants";
import { s } from "./styles";

/** The API codes a preview can end with, mapped to their message key. */
const ERROR_KEYS: Record<number, "notFound" | "tooLarge" | "invalidPath"> = {
  404: "notFound",
  413: "tooLarge",
  400: "invalidPath",
};

export function DocPreviewModal({ repoId, path, onClose }: { repoId: string; path: string; onClose: () => void }) {
  const t = useTranslations("context");
  const tc = useTranslations("common");
  const { data: doc, isLoading, isError, error, refetch } = useContextDoc(repoId, path);

  React.useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [onClose]);

  const errorKey = error instanceof ApiError ? ERROR_KEYS[error.status] : undefined;
  return (
    <Modal width={820} title={<span className="mono">{doc?.name ?? path}</span>} subtitle={doc ? path : undefined} onClose={onClose}>
      <div style={s.body}>
        {isLoading && <Skeleton height={120} />}
        {(isError || (!isLoading && !doc)) && (
          <ErrorState
            title={tc("states.error")}
            body={t(errorKey ? `preview.${errorKey}` : "preview.loadError")}
            onRetry={() => refetch()}
            retryLabel={tc("actions.retry")}
          />
        )}
        {doc && (
          <>
            <div style={s.meta}>
              <Badge color={DOC_TYPE_STYLE[doc.doc_type].color} bg={DOC_TYPE_STYLE[doc.doc_type].bg}>
                {t(`docType.${doc.doc_type}`)}
              </Badge>
              <span className="tnum">{t("preview.tokens", { tokens: doc.tokens })}</span>
            </div>
            <Markdown>{doc.content}</Markdown>
          </>
        )}
      </div>
    </Modal>
  );
}
