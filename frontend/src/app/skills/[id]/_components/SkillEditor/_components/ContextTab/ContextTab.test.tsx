/* Skill Editor → Context tab: "k attached" badge, the inheritance hint, the
   read-only "Serializes as" block (## Project context + one '- path' line per
   document), one PUT per change and no skill write (no new version). Real
   hooks over a stubbed API. */
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { renderWithProviders, screen, cleanup, waitFor, within } from "@/test/render";
import { mockFetch } from "@/test/fetch-mock";
import { makeSkill } from "@/test/skill-fixtures";
import { makeList, makePreview, makeRepo } from "@/test/context-fixtures";
import { RepoProvider } from "@/lib/repo-context";
import { ContextTab } from "./ContextTab";

vi.mock("next/navigation", () => ({ usePathname: () => "/skills/sk1" }));

let attached: string[];
let api: ReturnType<typeof mockFetch>;

beforeEach(() => {
  localStorage.clear();
  attached = ["specs/public-api.md"];
  api = mockFetch({
    "GET /repos": [makeRepo("r1", "payments-api")],
    "GET /repos/r1/context": makeList(),
    "GET /repos/r1/context/doc": (req) => makePreview(new URLSearchParams(req.search).get("path") ?? "", "# Public API\n\nRate-limit everything."),
    "GET /skills/sk1/context": () => ({ repo_id: "r1", paths: attached }),
    "PUT /skills/sk1/context": (req) => {
      attached = (req.body as { paths: string[] }).paths;
      return { repo_id: "r1", paths: attached };
    },
  });
});
afterEach(cleanup);

const renderTab = () =>
  renderWithProviders(
    <RepoProvider>
      <ContextTab skill={makeSkill({ id: "sk1" })} />
    </RepoProvider>,
  );

describe("Skill ContextTab", () => {
  it("shows '1 attached', the inheritance hint and the Serializes-as block; attaching saves the list without touching the skill (AC22)", async () => {
    const { user } = renderTab();
    expect(await screen.findByText("1 attached")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Project context to use" })).toBeInTheDocument();
    expect(screen.getByText("Any agent using this skill inherits these documents.")).toBeInTheDocument();
    const block = screen.getByLabelText("Serializes as");
    expect(block).toHaveTextContent("## Project context");
    expect(block).toHaveTextContent("- specs/public-api.md");
    expect(block).not.toHaveTextContent("Project specifications");

    await user.click(screen.getByRole("checkbox", { name: "architecture.md" }));
    await waitFor(() => expect(api.requests("PUT", "/skills/sk1/context")).toHaveLength(1));
    expect(api.requests("PUT", "/skills/sk1/context")[0]!.body).toEqual({
      repo_id: "r1",
      paths: ["specs/public-api.md", "docs/architecture.md"],
    });
    expect(await screen.findByText("2 attached")).toBeInTheDocument();
    expect(screen.getByLabelText("Serializes as")).toHaveTextContent("- docs/architecture.md");
    // Attachments are not part of the skill: no skill write, no version.
    expect(api.requests().filter((r) => r.method !== "GET" && r.path !== "/skills/sk1/context")).toEqual([]);
  });

  it("previews a document with the eye button", async () => {
    const { user } = renderTab();
    await screen.findByText("1 attached");
    await user.click(screen.getByRole("button", { name: "Preview public-api.md" }));
    const dialog = await screen.findByRole("dialog");
    expect(await within(dialog).findByRole("heading", { name: "Public API" })).toBeInTheDocument();
  });
});
