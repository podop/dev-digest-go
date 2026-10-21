/* SkillListItem — one skill in the editor sidebar: type icon, name, version,
   the shared SkillControls (switch + trash), a one-line description, the type
   and source badges and the stats footer (agents · pull · accept). The open
   skill is highlighted like the active AgentCard. Click / Enter / Space opens it. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Badge, Icon } from "@devdigest/ui";
import type { Skill, SkillStatsSummary } from "@devdigest/shared";
import { SKILL_TYPE_STYLE, SkillTypeBadge } from "@/components/skill-type-badge";
import { DeleteSkillModal } from "@/app/skills/_components/DeleteSkillModal";
import { SkillCardStats } from "@/app/skills/_components/SkillCardStats";
import { SkillControls } from "@/app/skills/_components/SkillControls";
import { s } from "./styles";

export function SkillListItem({
  skill,
  stats,
  active,
  onOpen,
  onDeleted,
}: {
  skill: Skill;
  /** Last-30-days pull / accept rates (GET /skills/stats); undefined while loading. */
  stats?: SkillStatsSummary;
  active: boolean;
  onOpen: (id: string) => void;
  onDeleted: () => void;
}) {
  const t = useTranslations("skills");
  const [deleting, setDeleting] = React.useState(false);
  const { icon, color } = SKILL_TYPE_STYLE[skill.type];
  const TypeIcon = Icon[icon];
  const imported = skill.source !== "manual";

  return (
    <>
      <div
        role="button"
        tabIndex={0}
        aria-label={skill.name}
        aria-current={active || undefined}
        data-testid="skill-list-item"
        onClick={() => onOpen(skill.id)}
        onKeyDown={(e) => {
          if (e.target === e.currentTarget && (e.key === "Enter" || e.key === " ")) {
            e.preventDefault();
            onOpen(skill.id);
          }
        }}
        style={s.item(active, skill.enabled)}
      >
        <div style={s.header}>
          <div style={s.iconBox(color)}>
            <TypeIcon size={15} />
          </div>
          <span className="mono" style={s.name}>
            {skill.name}
          </span>
          <span className="mono tnum" title={t("card.versionTitle", { version: skill.version })} style={s.version}>
            v{skill.version}
          </span>
          <SkillControls skill={skill} onDelete={() => setDeleting(true)} />
        </div>
        <div style={s.description}>{skill.description}</div>
        <div style={s.meta}>
          <SkillTypeBadge type={skill.type} />
          <span title={imported && skill.source_ref ? t("card.sourceTitle", { ref: skill.source_ref }) : undefined}>
            <Badge color={imported ? "var(--warn)" : "var(--text-muted)"} icon={imported ? "Upload" : undefined}>
              {t(`source.${skill.source}`)}
            </Badge>
          </span>
        </div>
        <SkillCardStats skill={skill} stats={stats} />
      </div>
      {deleting && (
        <DeleteSkillModal
          skill={skill}
          onClose={() => setDeleting(false)}
          onDeleted={() => {
            setDeleting(false);
            onDeleted();
          }}
        />
      )}
    </>
  );
}
