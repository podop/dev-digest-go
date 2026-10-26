/* ConnectionRow — one GitLab connection (FR33): name, base URL, the token badge, the repository count, Test, Edit
   and Delete, and its last test result. Badge states follow Settings A FR4: a failed test reads "Failed", a saved
   token "Token saved", otherwise "No token". */
"use client";

import { useTranslations } from "next-intl";
import { Button } from "@devdigest/ui";
import type { GitLabConnection } from "@/lib/types";
import type { TestEntry } from "../../useConnectionTests";
import { TestResult } from "../TestResult";
import { s } from "./styles";

interface Props {
  connection: GitLabConnection;
  test: TestEntry | undefined;
  onTest: () => void;
  onEdit: () => void;
  onDelete: () => void;
}

export function ConnectionRow({ connection, test, onTest, onEdit, onDelete }: Props) {
  const t = useTranslations("gitlabIntegration.row");
  const failed = test?.result != null && !test.result.ok;
  const tone = failed ? "crit" : connection.token_configured ? "ok" : "muted";
  const badge = failed ? t("failed") : connection.token_configured ? t("tokenSaved") : t("noToken");
  const pending = test?.pending ?? false;

  return (
    <li style={s.row}>
      <div style={s.head}>
        <span style={s.name}>{connection.name}</span>
        <span style={s.badge(tone)}>
          <span aria-hidden style={s.badgeDot(tone)} />
          {badge}
        </span>
        <span style={s.meta}>{t("repos", { count: connection.repo_count })}</span>
        <div style={s.actions}>
          <Button
            type="button"
            kind="secondary"
            size="sm"
            onClick={onTest}
            disabled={pending}
            aria-label={t("testLabel", { name: connection.name })}
          >
            {t("test")}
          </Button>
          <Button type="button" kind="ghost" size="sm" onClick={onEdit} aria-label={t("editLabel", { name: connection.name })}>
            {t("edit")}
          </Button>
          <Button type="button" kind="ghost" size="sm" onClick={onDelete} aria-label={t("deleteLabel", { name: connection.name })}>
            {t("delete")}
          </Button>
        </div>
      </div>
      <div style={s.url}>{connection.base_url}</div>
      {pending && (
        <div role="status" style={s.testing}>
          {t("testing")}
        </div>
      )}
      {!pending && test?.result && <TestResult result={test.result} />}
    </li>
  );
}
