/* HomeView — root screen and first-run gate (FR27): once secrets-status and repos have
   loaded it replaces the URL with the first unmet setup step, or with the first repo's PR
   list. While loading or redirecting it shows only the skeleton, so no home content flashes. */
"use client";

import React from "react";
import { useRouter } from "next/navigation";
import { useTranslations } from "next-intl";
import { EmptyState, Button, Skeleton } from "@devdigest/ui";
import { useRepos, useSecretsStatus } from "@/lib/hooks";
import { AppShell } from "@/components/app-shell";
import { PageContainer } from "@/components/page-shell";
import { destinationHref, homeDestination } from "./helpers";
import { s } from "./styles";

export function HomeView() {
  const t = useTranslations("home");
  const router = useRouter();
  const secrets = useSecretsStatus();
  const repos = useRepos();

  const destination = secrets.data && repos.data ? homeDestination(secrets.data, repos.data) : null;
  const href = destination ? destinationHref(destination) : null;
  const hasError = secrets.isError || repos.isError;

  React.useEffect(() => {
    if (href) router.replace(href);
  }, [href, router]);

  return (
    <AppShell crumb={[{ label: t("breadcrumb") }]}>
      <PageContainer title={t("title")} subtitle={t("subtitle")}>
        {hasError ? (
          <EmptyState
            icon="AlertTriangle"
            title={t("error.title")}
            body={t("error.body")}
            cta={t("error.retry")}
            onCta={() => {
              if (secrets.isError) void secrets.refetch();
              if (repos.isError) void repos.refetch();
            }}
          />
        ) : href && destination?.kind === "pulls" ? (
          <div>
            <p style={s.redirecting}>{t("redirecting")}</p>
            <Button kind="primary" onClick={() => router.push(href)}>
              {t("openRepo", { name: repos.data?.[0]?.full_name ?? "" })}
            </Button>
          </div>
        ) : (
          <div style={s.skeletons} aria-busy="true">
            <Skeleton height={20} width={240} />
            <Skeleton height={48} />
            <Skeleton height={48} />
          </div>
        )}
      </PageContainer>
    </AppShell>
  );
}
