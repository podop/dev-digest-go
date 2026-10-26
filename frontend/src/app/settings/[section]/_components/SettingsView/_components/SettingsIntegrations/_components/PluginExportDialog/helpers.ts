import type { PluginExportRequest } from "@devdigest/shared";
import { PLUGIN_EXPORT_NAME_MAX, PLUGIN_EXPORT_NAME_RE } from "@devdigest/shared/constants/plugins";

/** True when the name field holds a name the server would refuse (empty = default name = fine). */
export function isInvalidExportName(raw: string): boolean {
  const name = raw.trim();
  return name !== "" && (name.length > PLUGIN_EXPORT_NAME_MAX || !PLUGIN_EXPORT_NAME_RE.test(name));
}

interface Selection {
  /** Ids of every agent / skill of the workspace. */
  agentIds: readonly string[];
  skillIds: readonly string[];
  excludedAgents: ReadonlySet<string>;
  excludedSkills: ReadonlySet<string>;
}

const selected = (all: readonly string[], excluded: ReadonlySet<string>) => all.filter((id) => !excluded.has(id));

/** How many agents and skills the selection holds. */
export function selectionSize({ agentIds, skillIds, excludedAgents, excludedSkills }: Selection): number {
  return selected(agentIds, excludedAgents).length + selected(skillIds, excludedSkills).length;
}

/** The POST /plugins/export body. Everything selected sends no ids ("all"); otherwise only the
 *  groups that still hold something are sent, since the server rejects an empty id list. */
export function buildExportRequest(
  selection: Selection,
  options: { name: string; includeEvalCases: boolean; includeLearnings: boolean },
): PluginExportRequest {
  const name = options.name.trim();
  const request: PluginExportRequest = {
    include_eval_cases: options.includeEvalCases,
    include_learnings: options.includeLearnings,
  };
  if (name !== "") request.name = name;
  const agents = selected(selection.agentIds, selection.excludedAgents);
  const skills = selected(selection.skillIds, selection.excludedSkills);
  if (agents.length === selection.agentIds.length && skills.length === selection.skillIds.length) return request;
  if (agents.length > 0) request.agent_ids = agents;
  if (skills.length > 0) request.skill_ids = skills;
  return request;
}
