import { useTranslations } from "next-intl";
import { s } from "./styles";

export type KeyStatus = "connected" | "notConnected" | "failed";

const TONES = { connected: "ok", notConnected: "muted", failed: "crit" } as const;

/** FR4 badge: text plus a dot, never colour alone (NFR7). */
export function StatusBadge({ status }: { status: KeyStatus }) {
  const t = useTranslations("keyField");
  const tone = TONES[status];
  return (
    <span style={s.badge(tone)}>
      <span aria-hidden style={s.badgeDot(tone)} />
      {t(`badge.${status}`)}
    </span>
  );
}
