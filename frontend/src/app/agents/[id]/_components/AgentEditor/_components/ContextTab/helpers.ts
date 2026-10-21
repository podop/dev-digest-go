import type { AgentSkillLink, ContextDoc, Skill } from "@devdigest/shared";

/** Ids of the agent's ENABLED linked skills in link (prompt) order — the only
 *  skills whose attachments a run adds (FR5). */
export function enabledSkillIds(skills: readonly Skill[], links: readonly AgentSkillLink[]): string[] {
  const byId = new Map(skills.map((sk) => [sk.id, sk]));
  return [...links]
    .sort((a, b) => a.order - b.order)
    .filter((l) => byId.get(l.skill_id)?.enabled === true)
    .map((l) => l.skill_id);
}

/** Estimated tokens a run of the agent adds in this repo: its own attachments,
 *  then each skill's in order, each path counted once (the first occurrence
 *  wins). `fromSkills` is the part contributed by skill-only paths. A path the
 *  repo no longer lists counts 0. */
export function runTokens(
  docs: readonly ContextDoc[],
  direct: readonly string[],
  skillPaths: readonly (readonly string[])[],
): { total: number; fromSkills: number } {
  const tokens = new Map(docs.map((d) => [d.path, d.tokens]));
  const seen = new Set<string>();
  let total = 0;
  let fromSkills = 0;
  const add = (paths: readonly string[], viaSkill: boolean) => {
    for (const p of paths) {
      if (seen.has(p)) continue;
      seen.add(p);
      const n = tokens.get(p) ?? 0;
      total += n;
      if (viaSkill) fromSkills += n;
    }
  };
  add(direct, false);
  for (const paths of skillPaths) add(paths, true);
  return { total, fromSkills };
}
