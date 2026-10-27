/* ExportWizard — GitLab CI target (GitLab C FR40–FR43, AC38–AC40, AC43, AC16 box). The wizard runs for
   real against a stubbed fetch. Accessibility is asserted by role and accessible name (SCR4, no axe). */
import { describe, it, expect, afterEach } from "vitest";
import type { CiExport, CiFile } from "@devdigest/shared";
import type { Repo } from "@/lib/types";
import { renderWithProviders, screen, cleanup, waitFor, within, pseudoMessages } from "@/test/render";
import { mockFetch, type RecordedRequest } from "@/test/fetch-mock";
import { strayText } from "@/test/pseudo";
import { ExportWizard } from "./ExportWizard";

afterEach(cleanup);

const BASE: Repo = {
  id: "r1",
  workspace_id: "w1",
  owner: "acme",
  name: "api",
  full_name: "acme/api",
  default_branch: "main",
  clone_path: null,
  last_polled_at: null,
  created_by: null,
};
const GITHUB: Repo = { ...BASE, provider: "github", host: "github.com", connection_id: null };
const GITLAB: Repo = {
  ...BASE,
  id: "r2",
  owner: "grp",
  name: "svc",
  full_name: "grp/svc",
  provider: "gitlab",
  host: "gitlab.com",
  web_url: "https://gitlab.com/grp/svc",
  connection_id: "c1",
};
const GITLAB_OFF: Repo = {
  ...GITLAB,
  id: "r3",
  full_name: "grp/old",
  name: "old",
  host: "git.corp.example:8443",
  web_url: "https://git.corp.example:8443/grp/old",
  connection_id: null,
};

const JOB_PATH = ".gitlab/devdigest/review-synapse-bot.yml";
const SNIPPET = `include:\n  - local: ${JOB_PATH}\n`;
const MR_URL = "https://gitlab.com/grp/svc/-/merge_requests/7";
const FILES: CiFile[] = [
  { path: ".devdigest/agents/synapse-bot.yaml", contents: "name: synapse-bot\n", editable: false },
  { path: ".devdigest/runner/devdigest-runner-linux-amd64.gz.b64", contents: "H4sIAAAA", editable: false },
  { path: JOB_PATH, contents: "devdigest-review-synapse-bot:\n  stage: test\n", editable: true },
];
const TOKEN = "DEVDIGEST_GITLAB_TOKEN";

function answer(root: "create" | "keep_existing" | "custom_path", opts: { post_as?: string; configPath?: string } = {}): CiExport {
  return {
    installation: null,
    files: FILES,
    pr_url: MR_URL,
    required_secrets: opts.post_as === "none" ? ["OPENROUTER_API_KEY"] : ["OPENROUTER_API_KEY", TOKEN],
    required_variables: [],
    gitlab_ci: {
      job_file: JOB_PATH,
      root_config: root,
      config_path: opts.configPath ?? ".gitlab-ci.yml",
      include_snippet: SNIPPET,
      triggers_note: null,
    },
  };
}

function stub(root: "create" | "keep_existing" | "custom_path", repos: Repo[] = [GITHUB, GITLAB, GITLAB_OFF], configPath?: string) {
  return mockFetch({
    "GET /repos": repos,
    "GET /settings/custom-providers": [],
    "POST /agents/:id/export-ci": (req: RecordedRequest) =>
      answer(root, { post_as: (req.body as { post_as?: string }).post_as, configPath }),
  });
}

function open(extra: Partial<React.ComponentProps<typeof ExportWizard>> = {}, messages?: ReturnType<typeof pseudoMessages>) {
  return renderWithProviders(
    <ExportWizard agentId="a1" agentName="Synapse bot" provider="openrouter" onClose={() => {}} {...extra} />,
    messages ? { messages } : undefined,
  );
}

type User = ReturnType<typeof open>["user"];

const bodies = (http: ReturnType<typeof mockFetch>) =>
  http.requests("POST", "/agents/a1/export-ci").map((r) => r.body as Record<string, unknown>);

/** Pick the GitLab repository on the Target step and continue to the Preview. */
async function toPreview(user: User) {
  await user.click(await screen.findByRole("radio", { name: "grp/svc · gitlab.com" }));
  await user.click(screen.getByRole("button", { name: "Continue" }));
  await screen.findByRole("navigation", { name: "FILES TO CREATE" });
}

async function toConfigure(user: User) {
  await toPreview(user);
  await user.click(screen.getByRole("button", { name: "Continue" }));
  await screen.findByRole("group", { name: "Runner" });
}

/** SCR4 / AC43: every control the user can reach has an accessible name. */
function expectNamedControls(root: HTMLElement) {
  for (const role of ["radio", "checkbox", "button", "textbox", "link", "combobox"] as const) {
    for (const el of within(root).queryAllByRole(role)) expect(el, `${role} has no accessible name`).toHaveAccessibleName();
  }
}

describe("ExportWizard — GitLab target step (FR40, AC38)", () => {
  it("lists each repository with its host, disables the not-connected one with its reason, and sends repo_id + target gitlab_ci", async () => {
    const http = stub("create");
    const { user } = open();

    const group = await screen.findByRole("group", { name: "Target repository" });
    expect(within(group).getByRole("radio", { name: "acme/api" })).toBeEnabled();
    expect(within(group).getByRole("radio", { name: "grp/svc · gitlab.com" })).toBeEnabled();
    const off = within(group).getByRole("radio", { name: "grp/old · git.corp.example:8443" });
    expect(off).toBeDisabled();
    expect(off).toHaveAccessibleDescription("Not connected — add a GitLab connection for git.corp.example:8443");
    expect(screen.getByRole("button", { name: "Continue" })).toBeDisabled();

    await user.click(off); // a disabled repository cannot be chosen
    expect(off).not.toBeChecked();

    await user.click(within(group).getByRole("radio", { name: "grp/svc · gitlab.com" }));
    const targets = screen.getByRole("group", { name: "Target" });
    expect(within(targets).getByRole("radio", { name: /^GitLab CI/ })).toBeChecked();
    const gha = within(targets).getByRole("radio", { name: /^GitHub Actions/ });
    expect(gha).toBeDisabled();
    expect(gha).toHaveAccessibleName(expect.stringContaining("Needs a GitHub repository"));

    await user.click(within(group).getByRole("radio", { name: "acme/api" }));
    expect(within(targets).getByRole("radio", { name: /^GitHub Actions/ })).toBeChecked();
    expect(within(targets).getByRole("radio", { name: /^GitLab CI/ })).toBeDisabled();
    expect(within(targets).getByRole("radio", { name: /^GitLab CI/ })).toHaveAccessibleName(
      expect.stringContaining("Needs a GitLab repository"),
    );

    await user.click(within(group).getByRole("radio", { name: "grp/svc · gitlab.com" }));
    expectNamedControls(screen.getByRole("dialog"));
    await user.click(screen.getByRole("button", { name: "Continue" }));
    await screen.findByRole("navigation", { name: "FILES TO CREATE" });

    expect(bodies(http)).toEqual([
      {
        repo: "grp/svc",
        repo_id: "r2",
        target: "gitlab_ci",
        action: "files",
        post_as: "github_review",
        triggers: ["opened", "synchronize", "reopened"],
        runner: "github_hosted",
      },
    ]);
  });

  it("explains why a not-connected repository cannot be exported, even when it was the one Update came from", async () => {
    stub("create", [GITLAB_OFF]);
    open({ initialRepo: "grp/old" });

    const radio = await screen.findByRole("radio", { name: "grp/old · git.corp.example:8443" });
    expect(radio).toBeDisabled();
    expect(radio).not.toBeChecked();
    expect(radio).toHaveAccessibleDescription("Not connected — add a GitLab connection for git.corp.example:8443");
    expect(screen.getByRole("button", { name: "Continue" })).toBeDisabled();
  });
});

describe("ExportWizard — GitLab preview (FR41, AC39, AC16)", () => {
  it("marks the job file editable, derives the slug, and shows the include box with both warnings and a working copy button for keep_existing", async () => {
    stub("keep_existing");
    const { user } = open();
    await toPreview(user);

    const files = screen.getByRole("navigation", { name: "FILES TO CREATE" });
    const job = within(files).getByRole("button", { name: new RegExp(`^${JOB_PATH.replaceAll(".", "\\.")}`) });
    expect(job).toHaveAccessibleName(expect.stringContaining("editable"));
    expect(job).toHaveAttribute("aria-current", "true");
    expect(within(files).getByRole("button", { name: /gz\.b64/ })).not.toHaveAccessibleName(expect.stringContaining("editable"));
    expect(screen.getByRole("textbox", { name: "Workflow file contents" })).toHaveValue(
      "devdigest-review-synapse-bot:\n  stage: test\n",
    );

    const box = screen.getByRole("region", { name: "Root CI configuration include" });
    expect(box).toHaveTextContent("Your project already has .gitlab-ci.yml. Add this include to it:");
    expect(within(box).getByText(/local: \.gitlab\/devdigest\/review-synapse-bot\.yml/)).toBeInTheDocument();
    expect(within(box).getByText("If your workflow:rules exclude merge request pipelines, the review job cannot run")).toBeInTheDocument();
    expect(within(box).getByText("Your stages must include test, or change the job's stage")).toBeInTheDocument();

    await user.click(within(box).getByRole("button", { name: "Copy the include snippet" }));
    expect(await navigator.clipboard.readText()).toBe(SNIPPET);
    expect(await within(box).findByText("Copied")).toBeInTheDocument();

    // the runner bundle is listed, not rendered
    await user.click(within(files).getByRole("button", { name: /gz\.b64/ }));
    expect(screen.getByText("Runner bundle (8 B) — added as is, not shown here.")).toBeInTheDocument();
    expectNamedControls(screen.getByRole("dialog"));

    // the slug comes from the job file path
    await user.click(screen.getByRole("button", { name: "Continue" }));
    await user.click(await screen.findByRole("button", { name: "Continue" }));
    expect(screen.getByText(/Downloads devdigest-ci-synapse-bot\.zip with the same files/)).toBeInTheDocument();
  });

  it("names the project's own config path for custom_path and shows no box for create", async () => {
    stub("custom_path", [GITLAB], "ci/main.yml");
    const { user } = open();
    await toPreview(user);
    expect(screen.getByRole("region", { name: "Root CI configuration include" })).toHaveTextContent(
      "Your project already has ci/main.yml. Add this include to it:",
    );

    cleanup();
    stub("create", [GITLAB]);
    const second = open();
    await toPreview(second.user);
    expect(screen.queryByRole("region", { name: "Root CI configuration include" })).not.toBeInTheDocument();
  });
});

describe("ExportWizard — GitLab configure and install (FR42, FR43, AC40, AC43)", () => {
  it("shows the GitLab labels and the triggers note, then the merge request, variables, token hint, snippet and the Pipelines-must-succeed step", async () => {
    const http = stub("keep_existing");
    const { user } = open();
    await toConfigure(user);

    const dialog = screen.getByRole("dialog");
    const trigger = screen.getByRole("group", { name: "Trigger" });
    expect(within(trigger).queryAllByRole("button")).toHaveLength(0);
    expect(
      within(trigger).getByText("GitLab runs merge request pipelines on every push; trigger choices do not apply"),
    ).toBeInTheDocument();
    const runner = screen.getByRole("group", { name: "Runner" });
    expect(within(runner).getByRole("radio", { name: "GitLab shared runners" })).toBeChecked();
    expect(within(runner).getByRole("radio", { name: "Self-managed runner (tag devdigest)" })).not.toBeChecked();
    const postAs = screen.getByRole("group", { name: "Post results as" });
    expect(within(postAs).getByRole("radio", { name: /^MR review \(summary \+ inline threads\)/ })).toBeChecked();
    expect(within(postAs).getByRole("radio", { name: "MR comment (summary only)" })).toBeInTheDocument();
    expect(screen.getByText("OPENROUTER_API_KEY")).toBeInTheDocument();
    expect(screen.getByText(TOKEN)).toBeInTheDocument();
    expect(screen.queryByText("GITHUB_TOKEN")).not.toBeInTheDocument();
    expect(screen.getByText(/Pipelines must succeed/)).toBeInTheDocument();
    expectNamedControls(dialog);

    // "Don't post": the answer no longer lists the posting token
    await user.click(within(postAs).getByRole("radio", { name: "Don't post" }));
    await waitFor(() => expect(screen.queryByText(TOKEN)).not.toBeInTheDocument());
    expect(screen.getByText("OPENROUTER_API_KEY")).toBeInTheDocument();
    await user.click(within(postAs).getByRole("radio", { name: /^MR review/ }));
    expect(await screen.findByText(TOKEN)).toBeInTheDocument();
    await user.click(within(runner).getByRole("radio", { name: "Self-managed runner (tag devdigest)" }));
    await waitFor(() => expect(bodies(http).at(-1)).toMatchObject({ runner: "self_hosted" }));

    await user.click(screen.getByRole("button", { name: "Continue" }));
    expect(await screen.findByRole("radio", { name: /^Open a merge request with these files/ })).toBeChecked();
    expect(screen.getByRole("radio", { name: /^Copy files as a zip/ })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Install" }));

    expect(await screen.findByRole("heading", { name: "Merge request ready" })).toBeInTheDocument();
    expect(bodies(http).at(-1)).toMatchObject({
      action: "open_pr",
      repo: "grp/svc",
      repo_id: "r2",
      target: "gitlab_ci",
      runner: "self_hosted",
    });
    expect(screen.getByRole("link", { name: "View merge request" })).toHaveAttribute("href", MR_URL);
    const steps = screen.getAllByRole("list")[0]!; // the next steps; the variables follow
    expect(within(steps).getAllByRole("listitem").map((li) => li.textContent)).toEqual([
      "Add the CI/CD variables below first — the pipeline of the new merge request fails until they exist.",
      "Add the include shown below to .gitlab-ci.yml.",
      "Merge the merge request.",
      "To block merges on blockers, enable Settings › Merge requests › Pipelines must succeed.",
    ]);
    const variables = screen.getByRole("region", { name: "CI/CD variables expected" });
    expect(within(variables).getByText("OPENROUTER_API_KEY")).toBeInTheDocument();
    expect(variables).toHaveTextContent("Add it as a masked, not protected CI/CD variable");
    expect(variables).toHaveTextContent(
      "scope api; a project access token with the Reporter role is recommended (on gitlab.com project access tokens need Premium or Ultimate; otherwise use a personal access token of a dedicated user)",
    );
    expect(screen.getByRole("region", { name: "Root CI configuration include" })).toHaveTextContent(
      "Your stages must include test, or change the job's stage",
    );
    expectNamedControls(dialog);
  });

  it("omits the token hint and the include step for post_as none with a created root config", async () => {
    stub("create", [GITLAB]);
    const { user } = open();
    await toConfigure(user);
    await user.click(screen.getByRole("radio", { name: "Don't post" }));
    await waitFor(() => expect(screen.queryByText(TOKEN)).not.toBeInTheDocument());
    await user.click(screen.getByRole("button", { name: "Continue" }));
    await user.click(await screen.findByRole("button", { name: "Install" }));

    await screen.findByRole("heading", { name: "Merge request ready" });
    expect(screen.queryByText(TOKEN)).not.toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Root CI configuration include" })).not.toBeInTheDocument();
    expect(within(screen.getAllByRole("list")[0]!).getAllByRole("listitem")).toHaveLength(3);
  });
});

describe("ExportWizard — GitLab messages (NFR12, AC43)", () => {
  it("renders the GitLab copy of every step from the message files", async () => {
    stub("keep_existing");
    const { user } = open({}, pseudoMessages());
    const dialog = () => screen.getByRole("dialog");
    const continueName = "⟦ci.exportWizard.continue⟧";
    const allowed = [
      "acme/api",
      "grp/svc · gitlab.com",
      "grp/old · git.corp.example:8443",
      "OPENROUTER_API_KEY",
      TOKEN,
      JOB_PATH,
      ".devdigest/agents/synapse-bot.yaml",
      ".devdigest/runner/devdigest-runner-linux-amd64.gz.b64",
      ".gitlab-ci.yml",
      /^include:/,
      /^devdigest-review-/,
      /^\d$/, // the stepper's step numbers
    ];

    await user.click(await screen.findByRole("radio", { name: "grp/svc · gitlab.com" }));
    expect(strayText(dialog(), allowed)).toEqual([]);
    await user.click(screen.getByRole("button", { name: continueName }));
    await screen.findByText("⟦ci.exportWizard.filesToCreate⟧");
    await screen.findByRole("region", { name: "⟦ci.exportWizard.gitlab.includeBoxLabel⟧" });
    expect(strayText(dialog(), allowed)).toEqual([]);
    await user.click(screen.getByRole("button", { name: continueName }));
    await screen.findByText("⟦ci.exportWizard.runnerLabel⟧");
    expect(strayText(dialog(), allowed)).toEqual([]);
    await user.click(screen.getByRole("button", { name: continueName }));
    await screen.findByText("⟦ci.exportWizard.gitlab.secretNote⟧");
    expect(strayText(dialog(), allowed)).toEqual([]);
    await user.click(screen.getByRole("button", { name: "⟦ci.exportWizard.install⟧" }));
    await screen.findByText("⟦ci.exportWizard.gitlab.successTitle⟧");
    expect(strayText(dialog(), allowed)).toEqual([]);
  });
});
