/* AddRepoForm — the add-repository form (a GitHub or GitLab URL; API keys live in Settings → API
   Keys). Shared by Settings → GitHub Integration and the first-run Setup: the host decides
   what happens after an add (`onAdded`) and whether there is a Cancel (`onCancel`). A URL that
   `parseRepoUrl` refuses is rejected here with the FR12 text, without a request. Every other add
   error is shown once, here (`useAddRepo` is quiet): the API's message (duplicate, project not
   found / moved, permission, path conflict, clone error), "No GitLab connection for <host>" with
   a link to Settings → GitLab Integration, or a generic text for a 5xx. The URL is kept so it can
   be fixed. When the added repository belongs to another section than the one hosting the form
   (`section`, default GitHub), a toast says where it appears ("Added to GitLab · <host>"): the
   host closes the form on `onAdded`, so an in-form line would vanish, and the section's list does
   not show it. Focus goes to the URL field on open and back to it after a failure. */
"use client";

import React from "react";
import Link from "next/link";
import { useTranslations } from "next-intl";
import { Button } from "@devdigest/ui";
import { Callout, Field, TextField } from "@/components/settings-kit";
import { parseRepoUrl, REPO_URL_INVALID_MESSAGE } from "@devdigest/shared";
import { GITLAB_ERROR_CODES, codeHostName, isGitLab } from "@/lib/code-host";
import { useToast } from "@/lib/toast";
import { useAddRepo } from "@/lib/hooks";
import { ApiError } from "@/lib/api";
import type { Repo } from "@/lib/types";

/** What the form shows after a failed add. `notConnected` renders the host with the link to the GitLab section. */
type AddError = { kind: "text"; text: string } | { kind: "notConnected"; host: string | null; text: string };

const GITLAB_SETTINGS_HREF = "/settings/gitlab";
const SERVER_ERROR_STATUS = 500;
/** GitLab codes whose server message is specific and curated (rate limit, unavailable, …) — shown even on a 5xx. */
const GITLAB_CODES: ReadonlySet<string> = new Set(Object.values(GITLAB_ERROR_CODES));

function hostOf(details: unknown): string | null {
  if (typeof details !== "object" || details === null || !("host" in details)) return null;
  return typeof details.host === "string" && details.host !== "" ? details.host : null;
}

type AddRepoFormProps = {
  onAdded: (repo: Repo) => void;
  onCancel?: () => void;
  /** The section hosting the form (its list shows only that provider's repositories). Default: GitHub. */
  section?: "github" | "gitlab";
};

export function AddRepoForm({ onAdded, onCancel, section = "github" }: AddRepoFormProps) {
  const t = useTranslations("addRepo");
  const toast = useToast();
  const [repoUrl, setRepoUrl] = React.useState("");
  const [error, setError] = React.useState<AddError | null>(null);
  const formRef = React.useRef<HTMLFormElement>(null);
  const urlId = React.useId();
  const addRepo = useAddRepo();
  const url = repoUrl.trim();

  const focusUrl = () => formRef.current?.querySelector("input")?.focus();
  // Focus the field when the form opens (external DOM state, so an effect is right here).
  React.useEffect(focusUrl, []);

  const toAddError = (err: unknown): AddError => {
    if (!(err instanceof ApiError)) return { kind: "text", text: t("addError") };
    if (err.code === GITLAB_ERROR_CODES.hostNotConnected) {
      const host = hostOf(err.details);
      return { kind: "notConnected", host, text: err.message || t("addError") };
    }
    // A generic 5xx carries no useful text for the user; a GitLab one (rate limited, unavailable) does.
    if (err.status >= SERVER_ERROR_STATUS && !(err.code && GITLAB_CODES.has(err.code))) {
      return { kind: "text", text: t("serverError") };
    }
    return { kind: "text", text: err.message || t("addError") };
  };

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!url || addRepo.isPending) return;
    setError(null);
    if (parseRepoUrl(url) === null) {
      setError({ kind: "text", text: REPO_URL_INVALID_MESSAGE });
      focusUrl();
      return;
    }
    try {
      const repo = await addRepo.mutateAsync(url);
      setRepoUrl("");
      if ((isGitLab(repo) ? "gitlab" : "github") !== section) {
        toast.success(t("addedTo", { provider: codeHostName(repo), host: repo.host ?? "" }));
      }
      onAdded(repo);
    } catch (err) {
      setError(toAddError(err));
      focusUrl();
    }
  };

  return (
    <form ref={formRef} onSubmit={submit} noValidate>
      <Field label={t("urlLabel")} htmlFor={urlId} hint={t("urlHint")} style={{ marginBottom: 12 }}>
        <TextField
          id={urlId}
          value={repoUrl}
          onChange={setRepoUrl}
          mono
          invalid={error != null}
          placeholder={t("urlPlaceholder")}
        />
      </Field>

      {error && (
        <Callout tone="crit" style={{ marginBottom: 12 }}>
          {error.kind === "notConnected" && error.host ? t("hostNotConnected", { host: error.host }) : error.text}
          {error.kind === "notConnected" && (
            <>
              {" "}
              <Link href={GITLAB_SETTINGS_HREF} style={{ color: "var(--accent-text)", textDecoration: "underline" }}>
                {t("addConnection")}
              </Link>
            </>
          )}
        </Callout>
      )}

      <div style={{ display: "flex", gap: 8, alignItems: "center", marginBottom: 18 }}>
        <div style={{ flex: 1 }} />
        {onCancel && (
          <Button kind="ghost" size="sm" type="button" onClick={onCancel}>
            {t("cancel")}
          </Button>
        )}
        <Button kind="secondary" size="sm" icon="Plus" type="submit" disabled={!url || addRepo.isPending}>
          {addRepo.isPending ? t("submitting") : t("submit")}
        </Button>
      </div>
    </form>
  );
}
