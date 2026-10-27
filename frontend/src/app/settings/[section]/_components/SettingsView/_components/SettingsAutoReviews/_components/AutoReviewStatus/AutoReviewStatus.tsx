"use client";

import Link from "next/link";
import { useTranslations } from "next-intl";
import { Button } from "@devdigest/ui";
import { useAutomaticReviewsStatus } from "@/lib/hooks";
import { sectionHref } from "../../../../constants";
import { fixSectionsOf, pollAge, toneOf, type PollAge } from "./helpers";
import { s } from "./styles";

const LINK_KEY = { github: "links.github", gitlab: "links.gitlab", "api-keys": "links.apiKeys" } as const;

/** Message key of the "last poll" text for an age. */
function agePart(t: ReturnType<typeof useTranslations>, age: PollAge): string {
  return "count" in age ? t(`lastPoll.${age.unit}`, { count: age.count }) : t(`lastPoll.${age.unit}`);
}

/** FR17: the one-line status of automatic reviews. The hook refetches every 30 s while this is mounted,
 *  so the line is live exactly while the section is open (A12); a failed request shows Retry (EC18). */
export function AutoReviewStatus() {
  const t = useTranslations("autoReviewStatus");
  const tc = useTranslations("common");
  const status = useAutomaticReviewsStatus();
  // dataUpdatedAt changes on every refetch, so the "ago" text is recomputed each 30 s even when the data is equal.
  const { data, isError, dataUpdatedAt } = status;

  if (isError) {
    return (
      <div role="status" style={s.line("warn")}>
        <span aria-hidden style={s.dot("warn")} />
        <span>{t("unavailable")}</span>
        <Button kind="ghost" size="sm" onClick={() => void status.refetch()}>
          {tc("actions.retry")}
        </Button>
      </div>
    );
  }
  if (!data) {
    return (
      <div role="status" style={{ ...s.line("muted"), ...s.muted }}>
        {t("checking")}
      </div>
    );
  }

  const fixes = fixSectionsOf(data.state);
  const text =
    data.state === "active"
      ? t.rich("states.active", {
          b: (chunks) => <strong style={s.strong}>{chunks}</strong>,
          when: agePart(t, pollAge(data.last_poll_at, dataUpdatedAt)),
          count: data.prs_tracked,
        })
      : t(`states.${data.state}`);

  return (
    <div role="status" style={s.line(toneOf(data.state))}>
      <span aria-hidden style={s.dot(toneOf(data.state))} />
      <span>{text}</span>
      {fixes.map((fix) => (
        <Link key={fix} href={sectionHref(fix)} style={s.link}>
          {t(LINK_KEY[fix])}
        </Link>
      ))}
    </div>
  );
}
