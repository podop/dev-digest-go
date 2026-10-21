/* DocPreviewPane — the selected document: rendered as markdown or, for a store file
   in Edit mode, the editor over the draft the ContextView owns; with its token
   estimate, "Used by N agents" and a dirty dot. Raw HTML in a document is printed
   as text by the vendored <Markdown>, never executed. */
"use client";

import { useTranslations } from "next-intl";
import { Badge, EmptyState, ErrorState, Icon, Markdown, Skeleton } from "@devdigest/ui";
import { ApiError } from "@/lib/api";
import { useContextDoc } from "@/lib/hooks";
import { isDirty, type ContextMode } from "../../helpers";
import { DocEditor } from "../DocEditor";
import { ModeToggle } from "./_components/ModeToggle";
import { s } from "./styles";

/** The API codes a preview can end with, mapped to their message key. */
const ERROR_KEYS: Record<number, "notFound" | "tooLarge" | "invalidPath"> = {
  404: "notFound",
  413: "tooLarge",
  400: "invalidPath",
};

export function DocPreviewPane({
  repoId,
  path,
  mode,
  draft,
  onModeChange,
  onDraftChange,
  onSaved,
  onDiscard,
}: {
  repoId: string;
  path: string | null;
  mode: ContextMode;
  /** Unsaved text of the selected file, or null while nothing was typed. */
  draft: string | null;
  onModeChange: (mode: ContextMode) => void;
  onDraftChange: (text: string) => void;
  onSaved: (path: string, savedText: string) => void;
  onDiscard: () => void;
}) {
  const t = useTranslations("context");
  const tc = useTranslations("common");
  const { data: doc, isLoading, isError, error, refetch } = useContextDoc(repoId, path);

  if (!path) {
    return (
      <div style={s.pane}>
        <EmptyState icon="FileText" title={t("preview.select")} body={t("preview.selectBody")} />
      </div>
    );
  }
  if (isLoading) {
    return (
      <div style={s.pane}>
        <div style={s.center}>
          <Skeleton height={28} width="40%" />
          <Skeleton height={14} style={{ marginTop: 20 }} />
          <Skeleton height={14} width="80%" style={{ marginTop: 10 }} />
          <Skeleton height={14} width="65%" style={{ marginTop: 10 }} />
        </div>
      </div>
    );
  }
  if (isError || !doc) {
    const key = error instanceof ApiError ? ERROR_KEYS[error.status] : undefined;
    return (
      <div style={s.pane}>
        <ErrorState
          title={tc("states.error")}
          body={t(key ? `preview.${key}` : "preview.loadError")}
          onRetry={() => refetch()}
          retryLabel={tc("actions.retry")}
        />
      </div>
    );
  }

  const editing = mode === "edit" && doc.editable;
  const text = draft ?? doc.content;
  const usedByTitle = doc.used_by_agents
    .map((a) =>
      a.via === "skill"
        ? t("preview.usedBySkill", { name: a.name, skill: a.skill_name ?? "" })
        : t("preview.usedByDirect", { name: a.name }),
    )
    .join("\n");

  return (
    <div style={s.pane}>
      <div style={s.header}>
        <h2 className="mono" style={s.title} title={doc.path}>
          {doc.name}
        </h2>
        {isDirty(draft, doc.content) && (
          <span role="img" aria-label={t("editor.unsaved")} title={t("editor.unsaved")} style={s.dot} />
        )}
        <ModeToggle mode={editing ? "edit" : "preview"} canEdit={doc.editable} onChange={onModeChange} />
        <Badge>{t(`docType.${doc.doc_type}`)}</Badge>
        <div style={s.meta}>
          <span className="tnum">{t("preview.tokens", { tokens: doc.tokens })}</span>
          <span style={s.usedBy} title={usedByTitle || undefined}>
            <Icon.Cpu size={14} />
            {t("preview.usedBy", { count: doc.used_by })}
          </span>
        </div>
      </div>
      <div style={s.body}>
        {editing ? (
          <DocEditor
            key={doc.path}
            repoId={repoId}
            doc={doc}
            text={text}
            onChange={onDraftChange}
            onSaved={onSaved}
            onDiscard={onDiscard}
          />
        ) : (
          <Markdown>{text}</Markdown>
        )}
      </div>
    </div>
  );
}
