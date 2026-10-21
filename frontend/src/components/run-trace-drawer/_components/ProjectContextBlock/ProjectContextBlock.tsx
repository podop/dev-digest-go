/* ProjectContextBlock — the "Project context · attached specs" block of Prompt
   assembly: every attached document of the run, in prompt order, with its
   status; an included document opens to the exact text that was sent. The text
   is untrusted repo content, so it is only ever rendered as plain text. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Badge, Icon } from "@devdigest/ui";
import type { ProjectContextTrace, ProjectContextTraceDoc } from "@devdigest/shared";
import { CONTEXT_STATUS_COLORS, PROMPT_COLORS } from "../../constants";
import { formatApproxTokens } from "../../helpers";
import { s } from "../../styles";

function ContextDocItem({ doc }: { doc: ProjectContextTraceDoc }) {
  const t = useTranslations("runs");
  const [open, setOpen] = React.useState(false);
  const [copied, setCopied] = React.useState(false);
  const preId = React.useId();
  const colors = CONTEXT_STATUS_COLORS[doc.status];
  const hasText = doc.status === "included" && doc.text != null;
  const origin =
    doc.origin.kind === "skill"
      ? t("trace.projectContext.viaSkill", { name: doc.origin.skill_name })
      : t("trace.projectContext.viaAgent");
  const copy = () => {
    void navigator.clipboard?.writeText(doc.text ?? "");
    setCopied(true);
    setTimeout(() => setCopied(false), 1200);
  };
  return (
    <li style={s.ctxItem}>
      <div style={s.ctxItemHead}>
        {hasText ? (
          <button
            type="button"
            className="mono"
            aria-expanded={open}
            aria-controls={preId}
            onClick={() => setOpen((o) => !o)}
            style={s.ctxPath}
          >
            {doc.path}
          </button>
        ) : (
          <span className="mono" style={s.ctxPathStatic}>
            {doc.path}
          </span>
        )}
        <span style={s.ctxMeta}>
          <span>{doc.doc_type}</span>
          <span>{origin}</span>
          <span>{t("trace.projectContext.docTokens", { tokens: formatApproxTokens(doc.tokens) })}</span>
          <Badge color={colors.color} bg={colors.bg}>
            {t(`trace.projectContext.status.${doc.status}`)}
          </Badge>
          {hasText && (
            <button
              type="button"
              title={t("trace.projectContext.copy", { path: doc.path })}
              aria-label={t("trace.projectContext.copy", { path: doc.path })}
              onClick={copy}
              style={s.miniBtn}
            >
              {copied ? <Icon.Check size={12} /> : <Icon.Copy size={12} />}
            </button>
          )}
        </span>
      </div>
      {hasText && open && (
        <pre id={preId} className="mono" style={s.ctxPre}>
          {doc.text}
        </pre>
      )}
    </li>
  );
}

export function ProjectContextBlock({ context }: { context: ProjectContextTrace }) {
  const t = useTranslations("runs");
  return (
    <div style={s.promptRow}>
      <div style={s.ctxHead}>
        <span style={s.promptDot(PROMPT_COLORS.specs)} />
        <span style={s.ctxTitle}>{t("trace.projectContext.title")}</span>
        <span style={s.ctxTokens}>
          {t("trace.projectContext.tokens", {
            tokens: formatApproxTokens(context.tokens_total),
            budget: formatApproxTokens(context.budget_tokens),
          })}
        </span>
      </div>
      <ul style={{ listStyle: "none", margin: 0, padding: 0 }}>
        {context.docs.map((doc) => (
          <ContextDocItem key={`${doc.origin.kind}:${doc.path}`} doc={doc} />
        ))}
      </ul>
    </div>
  );
}
