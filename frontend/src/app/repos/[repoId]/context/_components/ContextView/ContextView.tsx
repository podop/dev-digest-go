/* ContextView — /repos/:repoId/context: the DevDigest store files (`.devdigest/specs/`,
   managed here) above the spec / doc / insight markdown files of the repo's clone
   (view-only), a filter, and a preview of the selected one.
   The selection and mode live in the URL (?doc=, ?mode=edit); the editor's unsaved
   draft lives here, so a file switch can ask before it drops it. */
"use client";

import React from "react";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import { EmptyState, ErrorState, Skeleton, TextInput } from "@devdigest/ui";
import { PROJECT_CONTEXT_STORE_ROOT } from "@devdigest/shared/constants/project-context";
import { AppShell } from "@/components/app-shell";
import { RepoNotFound } from "@/components/repo-not-found";
import { useContextDoc, useContextDocs, useRepos } from "@/lib/hooks";
import { useLeaveGuard } from "@/lib/leave-guard";
import { useRepoNotFound } from "@/lib/repo-context";
import { contextHref, filterDocs, isDirty, splitBySource, splitPath, type ContextMode } from "./helpers";
import { DocList } from "./_components/DocList";
import { DocPreviewPane } from "./_components/DocPreviewPane";
import { StoreSection } from "./_components/StoreSection";
import { s } from "./styles";

export function ContextView({ repoId, doc, mode = "preview" }: { repoId: string; doc: string | null; mode?: ContextMode }) {
  const t = useTranslations("context");
  const tc = useTranslations("common");
  const router = useRouter();
  const repoNotFound = useRepoNotFound(repoId);
  const { data: repos } = useRepos();
  const { data: list, isLoading, isError, refetch } = useContextDocs(repoId);
  const { data: loaded } = useContextDoc(repoId, doc);
  const [query, setQuery] = React.useState("");
  const [draft, setDraft] = React.useState<{ path: string; text: string } | null>(null);

  const repoName = repos?.find((r) => r.id === repoId)?.full_name;
  const crumb = [
    ...(repoName ? [{ label: repoName, mono: true }] : []),
    { label: t("title") },
    ...(doc ? [{ label: splitPath(doc).name, mono: true }] : []),
  ];
  const docs = list?.docs ?? [];
  const all = splitBySource(docs);
  const visible = splitBySource(filterDocs(docs, query));
  const globs = list?.globs.join(", ") ?? "";
  const draftText = draft !== null && draft.path === doc ? draft.text : null;
  const dirty = isDirty(draftText, loaded?.content ?? "");
  useLeaveGuard(dirty, t("editor.leaveConfirm"));

  const navigate = (path: string | null, next: ContextMode) =>
    router.replace(contextHref(repoId, path, next), { scroll: false });
  const go = (path: string | null, next: ContextMode = "preview") => {
    // Switching to another file drops the draft; ask first when it holds unsaved text.
    if (path !== null && path !== doc) {
      if (dirty && !window.confirm(t("editor.leaveConfirm"))) return;
      setDraft(null);
    }
    navigate(path, next);
  };
  /** A renamed file keeps its draft and selection without a confirmation. */
  const renamed = (from: string, to: string) => {
    if (from !== doc) return;
    setDraft((d) => (d !== null && d.path === from ? { path: to, text: d.text } : d));
    navigate(to, mode);
  };

  if (repoNotFound) {
    return (
      <AppShell crumb={crumb}>
        <RepoNotFound />
      </AppShell>
    );
  }

  return (
    <AppShell crumb={crumb}>
      <div style={s.page}>
        <aside style={s.side}>
          <div style={s.sideHead}>
            <div style={s.sideHeadText}>
              <div style={s.label}>{t("label")}</div>
              <div className="mono" style={s.globs}>
                {PROJECT_CONTEXT_STORE_ROOT}
              </div>
              {list && list.clone_status === "ready" && (
                <div className="tnum" style={s.summary}>
                  {t("summary", { count: docs.length, tokens: list.tokens_total })}
                </div>
              )}
            </div>
          </div>
          <StoreSection
            repoId={repoId}
            docs={visible.store}
            empty={!!list && all.store.length === 0}
            selected={doc}
            onGo={go}
            onRenamed={renamed}
            filter={
              docs.length > 0 ? (
                <TextInput value={query} onChange={setQuery} placeholder={t("filterPlaceholder")} aria-label={t("filterLabel")} />
              ) : null
            }
          />

          {isLoading && (
            <div style={s.rows}>
              <Skeleton height={34} />
              <Skeleton height={34} />
              <Skeleton height={34} />
            </div>
          )}
          {isError && (
            <ErrorState title={tc("states.error")} body={t("loadError")} onRetry={() => refetch()} retryLabel={tc("actions.retry")} />
          )}
          {list?.clone_status === "not_cloned" && (
            <EmptyState icon="Folder" title={t("notCloned.title")} body={t("notCloned.body")} />
          )}
          {list?.clone_status === "ready" && all.repo.length === 0 && (
            <EmptyState icon="FileText" title={t("empty.title", { globs })} body={t("empty.body", { globs })} />
          )}
          {all.repo.length > 0 && (
            <>
              <div style={s.label}>{t("store.repoLabel")}</div>
              {globs && (
                <div className="mono" style={s.globs}>
                  {globs}
                </div>
              )}
              {list?.truncated && <p style={s.muted}>{t("truncated", { count: docs.length })}</p>}
              {visible.repo.length === 0 ? (
                <p style={s.muted}>{t("noMatch")}</p>
              ) : (
                <DocList docs={visible.repo} selected={doc} onSelect={go} />
              )}
            </>
          )}
        </aside>
        <DocPreviewPane
          repoId={repoId}
          path={doc}
          mode={mode}
          draft={draftText}
          onModeChange={(next) => go(doc, next)}
          onDraftChange={(text) => doc && setDraft({ path: doc, text })}
          onSaved={(path, text) => setDraft((d) => (d !== null && d.path === path && d.text === text ? null : d))}
          onDiscard={() => setDraft(null)}
        />
      </div>
    </AppShell>
  );
}
