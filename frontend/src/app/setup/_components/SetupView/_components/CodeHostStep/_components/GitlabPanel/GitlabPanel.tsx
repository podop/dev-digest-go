"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Button, Icon } from "@devdigest/ui";
import { Callout, Field, HeadLink, RevealButton, TextField } from "@/components/settings-kit";
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
      <Field label={t("codeHost.gitlab.baseUrlLabel")} htmlFor={baseId} hint={t("codeHost.gitlab.baseUrlHint")}>
        <TextField
          id={baseId}
          mono
          value={baseUrl}
          onChange={setBaseUrl}
          autoComplete="off"
          spellCheck={false}
          invalid={error?.field === "base"}
        />
      </Field>

      <div style={s.field}>
        <div style={s.head}>
          <label htmlFor={tokenId} style={s.label}>
            {t("codeHost.gitlab.tokenLabel")}
          </label>
          {tokenPage && <HeadLink href={tokenPage}>{t("codeHost.gitlab.createToken")}</HeadLink>}
        </div>
        <TextField
          id={tokenId}
          variant="surface"
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
          invalid={error?.field === "token"}
          suffix={<RevealButton revealed={reveal} onToggle={() => setReveal((r) => !r)} label={t("codeHost.gitlab.reveal")} />}
        />
        <div style={s.foot}>
          <span style={s.hint}>
            <Icon.Lock size={11} style={s.hintIcon} aria-hidden />
            <span>
              {t("codeHost.gitlab.scopesHint")} {t("storedLocally")}
            </span>
          </span>
          <div style={s.submit}>
            <Button kind="secondary" size="sm" type="submit" icon={busy ? "RefreshCw" : "Play"} disabled={busy}>
              {busy ? t("codeHost.gitlab.saving") : t("codeHost.gitlab.saveAndTest")}
            </Button>
          </div>
        </div>
      </div>

      {error && <Callout tone="crit">{error.text}</Callout>}
      {result && !busy && <GitlabResult result={result} />}
    </form>
  );
}

/** FR34: who the token belongs to, its scopes, expiry and instance version, and one line per warning. */
function GitlabResult({ result }: { result: ConnTestResult }) {
  const t = useTranslations("setup");
  if (!result.ok) return <Callout tone="crit">{result.message}</Callout>;
  const scopes = result.scopes;
  return (
    <>
      <Callout tone="ok" style={s.result}>
        <div style={s.login}>
          {result.login ? t("codeHost.connectedAs", { login: result.login }) : result.message}
        </div>
        <div>{scopes == null ? t("codeHost.gitlab.scopesNotReported") : t("codeHost.gitlab.scopes", { scopes: scopes.join(" · ") })}</div>
        <div>{result.expires_at ? t("codeHost.gitlab.expires", { date: result.expires_at }) : t("codeHost.gitlab.noExpiry")}</div>
        <div>
          {result.instance_version ? t("codeHost.gitlab.version", { version: result.instance_version }) : t("codeHost.gitlab.versionUnknown")}
        </div>
      </Callout>
      {(result.warnings ?? []).map((w) => (
        <Callout key={w.code} tone="warn" style={s.result}>
          {w.message}
        </Callout>
      ))}
    </>
  );
}
