/* SpecsRead — the "Specs read" row of Configuration: every project-context
   document of the run (included ones with their tokens, skipped ones with a
   status badge). Traces saved before project context only have `specs_read`
   paths, which are listed as they are. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Badge } from "@devdigest/ui";
import type { RunTrace } from "@devdigest/shared";
import { CONTEXT_STATUS_COLORS } from "../../constants";
import { formatApproxTokens } from "../../helpers";
import { s } from "../../styles";

export function SpecsRead({ trace }: { trace: RunTrace }) {
  const t = useTranslations("runs");
  const docs = trace.project_context?.docs ?? [];
  if (docs.length === 0 && trace.specs_read.length === 0) {
    return <span style={s.specsNone}>{t("trace.config.none")}</span>;
  }
  if (docs.length === 0) {
    return (
      <>
        {trace.specs_read.map((path) => (
          <span key={path} className="mono" style={s.spec}>
            {path}
          </span>
        ))}
      </>
    );
  }
  return (
    <>
      {docs.map((doc) => {
        const colors = CONTEXT_STATUS_COLORS[doc.status];
        return (
          <span key={`${doc.origin.kind}:${doc.path}`} className="mono" style={s.spec}>
            {doc.path}
            {doc.status === "included" ? (
              <span style={s.specTokens}>
                {" · "}
                {t("trace.config.specTokens", { tokens: formatApproxTokens(doc.tokens) })}
              </span>
            ) : (
              <>
                {" "}
                <Badge color={colors.color} bg={colors.bg}>
                  {t(`trace.projectContext.status.${doc.status}`)}
                </Badge>
              </>
            )}
          </span>
        );
      })}
    </>
  );
}
