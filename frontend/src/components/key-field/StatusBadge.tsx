import { useTranslations } from "next-intl";
import { InlineStatus } from "@/components/settings-kit";

export type KeyStatus = "connected" | "notConnected" | "failed";

const TONES = { connected: "ok", notConnected: "muted", failed: "crit" } as const;

/** FR4 status inside the key input: text plus an icon, never colour alone (NFR7). */
export function StatusBadge({ status }: { status: KeyStatus }) {
  const t = useTranslations("keyField");
  return <InlineStatus tone={TONES[status]}>{t(`badge.${status}`)}</InlineStatus>;
}
