"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Button, Icon, TextInput } from "@devdigest/ui";
import { applyMaskedEdit, maskKey } from "@/components/key-field";
import { ApiError } from "@/lib/api";
import { useSaveGitLabConnection, useTestConnection } from "@/lib/hooks";
import type { ConnTestResult, GitLabConnection } from "@/lib/types";
import { DEFAULT_GITLAB_URL } from "../../../../constants";
import { connectionForUrl, gitlabTokenPageUrl } from "../../../../helpers";
import { s } from "./styles";

/** The GitLab half of the code-host step (FR38): base URL (default https://gitlab.com), the token and
 *  "Save and test". It creates the connection (the server names it after the host) or updates the one of
 *  that host, then runs the connection test and shows its outcome (FR34). A blank token re-tests a saved one. */
export function GitlabPanel({
  connections,
  result,
  onResult,
}: {
  connections: readonly GitLabConnection[];
  /** The last test of this view (kept by the step while the user looks at GitHub). */
  result: ConnTestResult | null;
  onResult: (result: ConnTestResult) => void;
}) {
  const t = useTranslations("setup");
  const baseId = React.useId();
  const tokenId = React.useId();
  const save = useSaveGitLabConnection();
  const test = useTestConnection();
  const [baseUrl, setBaseUrl] = React.useState(DEFAULT_GITLAB_URL);
  const [token, setToken] = React.useState("");
  const [reveal, setReveal] = React.useState(false);
  const [error, setError] = React.useState<{ text: string; field: "base" | "token" | "form" } | null>(null);

  const busy = save.isPending || test.isPending;
  const existing = connectionForUrl(connections, baseUrl);
  const tokenPage = gitlabTokenPageUrl(baseUrl);
  const { mutateAsync: saveAsync } = save;
  const { mutateAsync: testAsync } = test;

  const submit = async () => {
    if (busy) return;
    setError(null);
    const url = baseUrl.trim();
    const typed = token.trim();
    if (!url) return setError({ text: t("codeHost.gitlab.urlRequired"), field: "base" });
    if (!typed && !existing?.token_configured) {
      return setError({ text: t("codeHost.gitlab.tokenRequired"), field: "token" });
    }

    let id = existing?.id;
    if (typed) {
      try {
        const saved = existing
          ? await saveAsync({ mode: "update", id: existing.id, input: { base_url: url, token: typed } })
          : await saveAsync({ mode: "create", input: { base_url: url, token: typed } });
        id = saved.id;
      } catch (e) {
        const text = e instanceof ApiError ? e.message : t("codeHost.gitlab.saveFailed");
        return setError({ text, field: e instanceof ApiError && e.status === 409 ? "base" : "form" });
      }
    }
    if (!id) return;

    let next: ConnTestResult;
    try {
      next = await testAsync({ provider: "gitlab", connection_id: id });
    } catch (e) {
      next = { provider: "gitlab", ok: false, message: e instanceof ApiError ? e.message : t("codeHost.gitlab.testFailed") };
    }
    setToken("");
    onResult(next);
  };

  return (
    <form
      noValidate
      onSubmit={(e) => {
        e.preventDefault();
        void submit();
      }}
    >
      <div style={s.field}>
        <div style={s.head}>
          <label htmlFor={baseId} style={s.label}>
            {t("codeHost.gitlab.baseUrlLabel")}
          </label>
        </div>
        <TextInput
          id={baseId}
          mono
          value={baseUrl}
          onChange={setBaseUrl}
          autoComplete="off"
          spellCheck={false}
          aria-invalid={error?.field === "base"}
        />
        <div style={s.hint}>{t("codeHost.gitlab.baseUrlHint")}</div>
      </div>

      <div style={s.field}>
        <div style={s.head}>
          <label htmlFor={tokenId} style={s.label}>
            {t("codeHost.gitlab.tokenLabel")}
          </label>
          {tokenPage && (
            <a href={tokenPage} target="_blank" rel="noopener noreferrer" style={s.docs}>
              {t("codeHost.gitlab.createToken")}
              <Icon.ExternalLink size={12} />
            </a>
          )}
        </div>
        <div style={s.row}>
          <div style={s.grow}>
            <TextInput
              id={tokenId}
              mono
              value={reveal ? token : maskKey(token)}
              onChange={(v) => setToken(reveal ? v : applyMaskedEdit(token, v))}
              placeholder={existing?.token_configured ? t("codeHost.gitlab.tokenSaved") : t("codeHost.gitlab.tokenEmpty")}
              autoComplete="off"
              autoCapitalize="off"
              autoCorrect="off"
              spellCheck={false}
              data-1p-ignore
              data-lpignore="true"
              aria-invalid={error?.field === "token"}
              suffix={
                <button
                  type="button"
                  style={s.toggle}
                  aria-label={t("codeHost.gitlab.reveal")}
                  aria-pressed={reveal}
                  onClick={() => setReveal((r) => !r)}
                >
                  {reveal ? <Icon.Eye size={14} /> : <Icon.EyeOff size={14} />}
                </button>
              }
            />
          </div>
          <Button kind="secondary" size="md" type="submit" disabled={busy}>
            {busy ? t("codeHost.gitlab.saving") : t("codeHost.gitlab.saveAndTest")}
          </Button>
        </div>
        <div style={s.hint}>
          {t("codeHost.gitlab.scopesHint")} {t("storedLocally")}
        </div>
      </div>

      {error && (
        <div role="alert" style={s.error}>
          <Icon.XCircle size={13} />
          {error.text}
        </div>
      )}
      {result && !busy && <GitlabResult result={result} />}
    </form>
  );
}

/** FR34: who the token belongs to, its scopes, expiry and instance version, and one line per warning. */
function GitlabResult({ result }: { result: ConnTestResult }) {
  const t = useTranslations("setup");
  if (!result.ok) {
    return (
      <div role="alert" style={s.error}>
        <Icon.XCircle size={13} />
        {result.message}
      </div>
    );
  }
  const scopes = result.scopes;
  return (
    <div role="status" style={s.who}>
      <div style={s.login}>
        {result.login ? t("codeHost.connectedAs", { login: result.login }) : result.message}
      </div>
      <div>{scopes == null ? t("codeHost.gitlab.scopesNotReported") : t("codeHost.gitlab.scopes", { scopes: scopes.join(" · ") })}</div>
      <div>{result.expires_at ? t("codeHost.gitlab.expires", { date: result.expires_at }) : t("codeHost.gitlab.noExpiry")}</div>
      <div>
        {result.instance_version ? t("codeHost.gitlab.version", { version: result.instance_version }) : t("codeHost.gitlab.versionUnknown")}
      </div>
      {(result.warnings ?? []).map((w) => (
        <div key={w.code} style={s.warn}>
          <Icon.AlertTriangle size={14} style={s.warnIcon} />
          <span>{w.message}</span>
        </div>
      ))}
    </div>
  );
}
