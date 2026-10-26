/* SettingsGitlab — Settings → GitLab Integration (FR33–FR36): the connection list with its test results (the first 5
   connections with a token are tested on open), the add / edit / delete dialogs and the workspace's GitLab
   repositories (FR37). Data only through the gitlab-connections and repo hooks. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Button, ErrorState, Skeleton } from "@devdigest/ui";
import { useGitLabConnections } from "@/lib/hooks";
import type { GitLabConnection } from "@/lib/types";
import { SectionTitle } from "../SectionTitle";
import { ConnectionDialog } from "./_components/ConnectionDialog";
import { ConnectionRow } from "./_components/ConnectionRow";
import { DeleteConnectionDialog } from "./_components/DeleteConnectionDialog";
import { GitLabRepos } from "./_components/GitLabRepos";
import { useConnectionTests } from "./useConnectionTests";
import { s } from "./styles";

type Dialog =
  | { kind: "add" }
  | { kind: "edit"; connection: GitLabConnection }
  | { kind: "delete"; connection: GitLabConnection };

export function SettingsGitlab() {
  const t = useTranslations("gitlabIntegration");
  const list = useGitLabConnections();
  const tests = useConnectionTests(list.data);
  const [dialog, setDialog] = React.useState<Dialog | null>(null);
  const close = () => setDialog(null);

  return (
    <div style={s.wrap}>
      <SectionTitle title={t("title")} body={t("body")} />
      <div style={s.head}>
        <span style={s.spacer} />
        <Button type="button" kind="secondary" size="sm" icon="Plus" onClick={() => setDialog({ kind: "add" })}>
          {t("add")}
        </Button>
      </div>
      {list.isError ? (
        <ErrorState title={t("loadFailedTitle")} retryLabel={t("retry")} onRetry={() => void list.refetch()} />
      ) : list.isPending ? (
        <div style={s.skeletons} aria-hidden>
          <Skeleton height={72} />
          <Skeleton height={72} />
        </div>
      ) : list.data.length === 0 ? (
        <p style={s.empty}>{t("empty")}</p>
      ) : (
        <ul style={s.list} aria-label={t("listLabel")}>
          {list.data.map((c) => (
            <ConnectionRow
              key={c.id}
              connection={c}
              test={tests.entries[c.id]}
              onTest={() => void tests.run(c.id)}
              onEdit={() => setDialog({ kind: "edit", connection: c })}
              onDelete={() => setDialog({ kind: "delete", connection: c })}
            />
          ))}
        </ul>
      )}
      <GitLabRepos />
      {dialog?.kind === "add" && <ConnectionDialog onClose={close} onTested={tests.record} />}
      {dialog?.kind === "edit" && (
        <ConnectionDialog key={dialog.connection.id} connection={dialog.connection} onClose={close} onTested={tests.record} />
      )}
      {dialog?.kind === "delete" && <DeleteConnectionDialog connection={dialog.connection} onClose={close} />}
    </div>
  );
}
