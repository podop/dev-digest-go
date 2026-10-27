import type { CiRepoEntry, CiRepoInstallation, CiRepoLastRun } from "@devdigest/shared";
import { safeRunHref } from "@/lib/ci-runs";

const MINUTE = 60;
const HOUR = 3600;
const DAY = 86400;
const MONTH = 30 * DAY;
const YEAR = 365 * DAY;

/** "3 hours ago" style text for an ISO time; null when missing or not a date. `now` is injectable for tests. */
export function relativeTime(iso: string | null | undefined, now: number = Date.now()): string | null {
  if (!iso) return null;
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return null;
  const diff = Math.round((then - now) / 1000);
  const abs = Math.abs(diff);
  const rtf = new Intl.RelativeTimeFormat("en", { numeric: "auto" });
  if (abs < MINUTE) return rtf.format(diff, "second");
  if (abs < HOUR) return rtf.format(Math.round(diff / MINUTE), "minute");
  if (abs < DAY) return rtf.format(Math.round(diff / HOUR), "hour");
  if (abs < MONTH) return rtf.format(Math.round(diff / DAY), "day");
  if (abs < YEAR) return rtf.format(Math.round(diff / MONTH), "month");
  return rtf.format(Math.round(diff / YEAR), "year");
}

/** Translation key (under `card.status`) of a run status; "unknown" for a status this UI does not know. */
export type RunStatusKey = "succeeded" | "noFindings" | "failed" | "running" | "unknown";

export function runStatusKey(status: string | null | undefined): RunStatusKey {
  switch (status) {
    case "succeeded":
      return "succeeded";
    case "no_findings":
      return "noFindings";
    case "failed":
      return "failed";
    case "running":
      return "running";
    default:
      return "unknown";
  }
}

/** What the row says about the last run: nothing ("no runs yet") when there is no run or it has no time. */
export interface LastRunView {
  statusKey: RunStatusKey;
  /** Relative time, "3 hours ago". */
  time: string;
  /** The run's absolute local time, for the native tooltip. */
  absolute: string;
  /** http(s) link to the run, or null — it is GitHub data, any other scheme is never rendered as a link. */
  href: string | null;
}

export function lastRunView(run: CiRepoLastRun | null, now: number = Date.now()): LastRunView | null {
  if (!run) return null;
  const time = relativeTime(run.ran_at, now);
  if (time === null || run.ran_at === null) return null;
  return {
    statusKey: runStatusKey(run.status),
    time,
    absolute: new Date(run.ran_at).toLocaleString(),
    href: safeRunHref(run.github_url),
  };
}

/** Whether the entry is a GitLab CI repository (FR46: `target_type` / `provider`, absent on pre-GitLab payloads). */
export function isGitLabEntry(entry: Pick<CiRepoEntry, "provider" | "target_type">): boolean {
  return entry.target_type === "gitlab_ci" || entry.provider === "gitlab";
}

/** React key of a row: a repository is identified by host + name (two hosts can share a name, FR46). */
export function entryKey(entry: Pick<CiRepoEntry, "repo" | "host" | "target_type">): string {
  return `${entry.host ?? ""}|${entry.target_type ?? ""}|${entry.repo}`;
}

/** The agent editor's CI tab (`/agents/:id?tab=ci`). */
export function manageHref(agentId: string): string {
  return `/agents/${encodeURIComponent(agentId)}?tab=ci`;
}

export interface ManageAgent {
  id: string;
  name: string;
}

/** The distinct agents installed in a repository, in installation order. */
export function installedAgents(installations: readonly CiRepoInstallation[]): ManageAgent[] {
  const seen = new Set<string>();
  const out: ManageAgent[] = [];
  for (const inst of installations) {
    if (seen.has(inst.agent_id)) continue;
    seen.add(inst.agent_id);
    out.push({ id: inst.agent_id, name: inst.agent_name });
  }
  return out;
}
