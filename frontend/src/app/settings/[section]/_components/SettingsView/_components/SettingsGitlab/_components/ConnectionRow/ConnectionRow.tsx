/* ConnectionRow — one GitLab connection (FR33): name, base URL, the token badge, the repository count, Test, Edit
   and Delete, and its last test result. Badge states follow Settings A FR4: a failed test reads "Failed", a saved
   token "Token saved", otherwise "No token". Laid out as the prototype's repository rows. */
"use client";

import { useTranslations } from "next-intl";
import { Icon } from "@devdigest/ui";
import { KitBadge, MonoAction, RowMeta } from "@/components/settings-kit";
import type { GitLabConnection } from "@/lib/types";
import type { TestEntry } from "../../useConnectionTests";
import { TestResult } from "../TestResult";
import { s } from "./styles";

/** The message key of the identity line (FR22): a read-only token wins (EC27); a login needs a successful test. */
function identityLine(test: TestEntry | undefined): { key: "identityReadOnly" | "identityUser" | "identityUnknown"; login: string } {
  const result = test?.result;
  if (result?.ok && result.token_access === "read_only") return { key: "identityReadOnly", login: "" };
  if (result?.ok && result.login) return { key: "identityUser", login: result.login };
  return { key: "identityUnknown", login: "" };
}

const TONE_COLOR = { ok: "var(--ok)", muted: "var(--text-muted)", crit: "var(--crit)" } as const;

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
  const identity = identityLine(test);

  return (
    <li style={s.row}>
      <div style={s.head}>
        <Icon.Globe size={13} style={s.icon} aria-hidden />
        <span style={s.name}>{connection.name}</span>
        <KitBadge color={TONE_COLOR[tone]} bg="transparent" dot>
          {badge}
        </KitBadge>
        <RowMeta>{t("repos", { count: connection.repo_count })}</RowMeta>
        <div style={s.actions}>
          <MonoAction onClick={onTest} disabled={pending} aria-label={t("testLabel", { name: connection.name })}>
            {t("test")}
          </MonoAction>
          <MonoAction onClick={onEdit} aria-label={t("editLabel", { name: connection.name })}>
            {t("edit")}
          </MonoAction>
          <MonoAction tone="crit" onClick={onDelete} aria-label={t("deleteLabel", { name: connection.name })}>
            {t("delete")}
          </MonoAction>
        </div>
      </div>
      <div style={s.body}>
        <div className="mono" style={s.url}>
          {connection.base_url}
        </div>
        <div style={s.identity}>{t(identity.key, { login: identity.login })}</div>
        {pending && (
          <div role="status" style={s.testing}>
            {t("testing")}
          </div>
        )}
        {!pending && test?.result && <TestResult result={test.result} />}
      </div>
    </li>
  );
}
