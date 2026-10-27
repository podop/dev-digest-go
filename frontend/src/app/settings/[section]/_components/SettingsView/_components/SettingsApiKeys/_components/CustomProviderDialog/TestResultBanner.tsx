import { useTranslations } from "next-intl";
import { Icon } from "@devdigest/ui";
import type { ConnTestResult } from "@/lib/types";
import { s } from "./styles";

/**
 * The FR6 banners of Settings A for a custom provider's test (FR23): verified / not listed (status), a result
 * with `warning` (status, warning tone), failure (alert). The provider is named by `name`, never by a message
 * key, because a custom id is not a message key.
 */
export function TestResultBanner({ result, name }: { result: ConnTestResult; name: string }) {
  const t = useTranslations("customProviders.test");
  const { model } = result;

  if (!result.ok) {
    return (
      <div role="alert" style={s.banner("crit")}>
        <Icon.XCircle size={15} style={s.bannerIcon("crit")} aria-hidden />
        {result.message}
      </div>
    );
  }
  if (result.warning) {
    return (
      <div role="status" style={s.banner("warn")}>
        <Icon.AlertTriangle size={15} style={s.bannerIcon("warn")} aria-hidden />
        {result.warning}
      </div>
    );
  }
  if (model && result.model_reachable === false) {
    return (
      <div role="status" style={s.banner("warn")}>
        <Icon.AlertTriangle size={15} style={s.bannerIcon("warn")} aria-hidden />
        {t("notListed", { model, provider: name })}
      </div>
    );
  }
  const text =
    model && result.model_reachable === true
      ? result.latency_ms == null
        ? t("verifiedNoLatency", { model })
        : t("verified", { model, latency: result.latency_ms })
      : result.message;
  return (
    <div role="status" style={s.banner("ok")}>
      <Icon.CheckCircle size={15} style={s.bannerIcon("ok")} aria-hidden />
      {text}
    </div>
  );
}

