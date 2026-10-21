/* PriorPrs — "Prior PRs touching these files" (client/specs/07-blast-radius.md,
   P3): a bordered accordion with a count badge that opens a timeline. Each row:
   #number + title (a github.com link built from the PR's own repo), avatar +
   author · date, then the PR's notes — or, with none, one prose line naming the overlapping files
   (basenames, the full paths in the tooltip).
   Rendered only when there is something to show: a loading, failed or empty
   history leaves no trace in the card. Layout from the design mockup's
   HistoryAccordion / HistoryRow. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import type { PrHistoryItem } from "@devdigest/shared";
import { Avatar, Badge, Icon } from "@devdigest/ui";
import { usePrHistory } from "@/lib/hooks/blast";
import { githubPrUrl } from "@/lib/github-urls";
import { FILES_SHOWN } from "./constants";
import { fileBasename } from "./helpers";
import { s } from "./styles";

export interface PriorPrsProps {
  prId: string;
  repoFullName: string | null;
}

function formatWhen(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleDateString();
}

export function PriorPrs({ prId, repoFullName }: PriorPrsProps) {
  const t = useTranslations("blast");
  const { data } = usePrHistory(prId);
  const [open, setOpen] = React.useState(false);
  const listId = React.useId();

  const items = data?.history ?? [];
  if (items.length === 0) return null;

  return (
    <div style={s.root}>
      <div style={s.accordion}>
        <button type="button" aria-expanded={open} aria-controls={listId} onClick={() => setOpen((o) => !o)} style={s.toggle(open)}>
          <Icon.History size={14} style={s.icon} />
          <span style={s.title}>{t("priorPrs.title")}</span>
          <Badge>{items.length}</Badge>
          <Icon.ChevronDown size={15} style={s.chevron(open)} />
        </button>
        {open && (
          <ul id={listId} style={s.list}>
            {items.map((pr, i) => (
              <HistoryRow key={pr.pr_number} pr={pr} repoFullName={repoFullName} last={i === items.length - 1} />
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}

function HistoryRow({ pr, repoFullName, last }: { pr: PrHistoryItem; repoFullName: string | null; last: boolean }) {
  const t = useTranslations("blast");
  return (
    <li style={s.row(last)}>
      <div style={s.rail} aria-hidden="true">
        <span style={s.dot} />
        {!last && <span style={s.line} />}
      </div>
      <div style={s.body}>
        <div style={s.titleLine}>
          <span className="mono" style={s.number}>
            #{pr.pr_number}
          </span>
          {repoFullName ? (
            <a href={githubPrUrl(repoFullName, pr.pr_number)} target="_blank" rel="noopener noreferrer" style={s.link}>
              {pr.title}
            </a>
          ) : (
            <span style={s.prTitle}>{pr.title}</span>
          )}
        </div>
        <div style={s.meta}>
          <span aria-hidden="true" style={s.avatar}>
            <Avatar name={pr.author} size={15} />
          </span>
          {t("priorPrs.meta", { author: pr.author, date: formatWhen(pr.merged_at) })}
        </div>
        {pr.notes.trim() ? (
          <p style={s.notes}>{pr.notes}</p>
        ) : (
          <p style={s.notes} title={pr.files_overlap.join("\n")}>
            {t("priorPrs.touched", {
              count: pr.files_overlap.length,
              names: pr.files_overlap.slice(0, FILES_SHOWN).map(fileBasename).join(", "),
              more: Math.max(0, pr.files_overlap.length - FILES_SHOWN),
            })}
          </p>
        )}
      </div>
    </li>
  );
}
