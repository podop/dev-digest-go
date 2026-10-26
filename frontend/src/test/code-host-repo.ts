/* test/code-host-repo.ts — repositories for the host-aware link tests (GitLab A): a GitLab repository whose
   web_url carries a subpath, and a GitHub one without web_url (what a pre-GitLab payload looks like). The tests
   mount `RepoProvider` with `GET /repos` answering one of them, so components read it through the real context. */
import type { Repo } from "@devdigest/shared";

export const GITLAB_REPO = {
  id: "r1",
  owner: "acme/platform",
  name: "payments",
  full_name: "acme/platform/payments",
  provider: "gitlab",
  host: "git.corp.example",
  web_url: "https://git.corp.example/gitlab/acme/platform/payments",
  connection_id: "c1",
} as Repo;

export const GITHUB_REPO = { id: "r1", owner: "acme", name: "api", full_name: "acme/api" } as Repo;
