/* StaleBanner — shown over the editor when a save was refused: the file changed
   elsewhere (stale), no longer exists (gone) or was re-created meanwhile (exists).
   Always Reload (drop the draft) / Keep editing; the banner never touches the text. */
"use client";

import { useTranslations } from "next-intl";
import { Button } from "@devdigest/ui";
import type { SaveProblem } from "../../helpers";
import { s } from "./styles";

export function StaleBanner({
  problem,
  busy,
  onReload,
  onKeep,
}: {
  problem: SaveProblem;
  busy: boolean;
  onReload: () => void;
  onKeep: () => void;
}) {
  const t = useTranslations("context");
  return (
    <div role="alert" style={s.banner}>
      <p style={s.text}>{t(`editor.banner.${problem}`)}</p>
      <div style={s.actions}>
        <Button kind="ghost" size="sm" disabled={busy} onClick={onReload}>
          {t("editor.banner.reload")}
        </Button>
        <Button size="sm" disabled={busy} onClick={onKeep}>
          {t("editor.banner.keep")}
        </Button>
      </div>
    </div>
  );
}
