import { useTranslations } from "next-intl";
import { Icon } from "@devdigest/ui";
import type { ConnTestResult } from "@/lib/types";
import { s } from "./styles";

/** FR6: verified / not listed / failure box (prototype "Connection verified" callout) for one test result. Success is a `status`, failure an `alert`. */
export function ResultBanner({ result }: { result: ConnTestResult }) {
  const t = useTranslations("keyField");
  const { model } = result;

  if (!result.ok) {
    return (
      <div role="alert" style={s.banner("crit")}>
        <Icon.XCircle size={15} style={s.bannerIcon("crit")} aria-hidden />
        {result.message}
      </div>
    );
  }
  if (model && result.model_reachable === false) {
    return (
      <div role="status" style={s.banner("warn")}>
        <Icon.AlertTriangle size={15} style={s.bannerIcon("warn")} aria-hidden />
        {t("result.notListed", { model, provider: t(`providers.${result.provider}`) })}
      </div>
    );
  }
  const text =
    model && result.model_reachable === true
      ? result.latency_ms == null
        ? t("result.verifiedNoLatency", { model })
        : t("result.verified", { model, latency: result.latency_ms })
      : result.message;
  return (
    <div role="status" style={s.banner("ok")}>
      <Icon.CheckCircle size={15} style={s.bannerIcon("ok")} aria-hidden />
      {text}
    </div>
  );
}
