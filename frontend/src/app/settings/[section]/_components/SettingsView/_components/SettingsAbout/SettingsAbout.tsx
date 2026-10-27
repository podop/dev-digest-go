"use client";

import Link from "next/link";
import { useTranslations } from "next-intl";
import { ErrorState, Icon, Skeleton } from "@devdigest/ui";
import { SectionPane } from "@/components/settings-kit";
import { useAbout } from "@/lib/hooks";
import { SectionTitle } from "../SectionTitle";
import { s } from "./styles";

/** Where "Run setup again" lands (FR21, D14). */
const SETUP_HREF = "/settings/setup?step=model";
const SKELETON_ROWS = [0, 1, 2, 3, 4];

/**
 * Settings → About: version, build, runner, config format and data directory from
 * GET /about (FR21). Values are shown as is (`dev` included) and never stored; there is
 * no "Updated" row (D20).
 */
export function SettingsAbout() {
  const t = useTranslations("about");
  const { data, isLoading, isError, refetch } = useAbout();

  return (
    <SectionPane maxWidth={560}>
      <SectionTitle title={t("title")} body={t("body")} />

      {isLoading && (
        <div role="status" aria-busy="true" aria-label={t("loading")} style={s.skeletons}>
          {SKELETON_ROWS.map((i) => (
            <Skeleton key={i} height={42} />
          ))}
        </div>
      )}
      {isError && <ErrorState title={t("loadError")} onRetry={() => refetch()} retryLabel={t("retry")} />}
      {data && (
        <div style={s.box}>
          <table style={s.table} aria-label={t("tableLabel")}>
            <tbody>
              <Row label={t("rows.version")} value={data.version} />
              <Row label={t("rows.build")} value={data.build_id} />
              <Row label={t("rows.runner")} value={t("runnerValue", { version: data.runner.version })} />
              <Row label={t("rows.configFormat")} value={data.config_format} />
              <Row label={t("rows.dataDir")} value={data.data_dir} last />
            </tbody>
          </table>
        </div>
      )}

      <Link href={SETUP_HREF} style={s.link}>
        {t("setupAgain")}
        <Icon.ArrowRight size={11} aria-hidden />
      </Link>
    </SectionPane>
  );
}

/** One row of the prototype's About box: 130px label column, mono 600 value. */
function Row({ label, value, last }: { label: string; value: string; last?: boolean }) {
  return (
    <tr>
      <th scope="row" style={s.rowHead(!!last)}>
        {label}
      </th>
      <td className="mono" style={s.cell(!!last)}>
        {value}
      </td>
    </tr>
  );
}
