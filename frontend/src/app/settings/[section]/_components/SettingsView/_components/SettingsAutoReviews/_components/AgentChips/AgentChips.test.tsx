import { describe, it, expect, afterEach, vi } from "vitest";
import { renderWithProviders, screen, cleanup, pseudoMessages } from "@/test/render";
import { strayText } from "@/test/pseudo";
import type { Agent } from "@devdigest/shared";
import { AgentChips } from "./AgentChips";

afterEach(cleanup);

function agent(id: string, name: string, enabled = true): Agent {
  return {
    id,
    name,
    description: "",
    provider: "openai",
    model: "gpt-4.1",
    system_prompt: "x",
    enabled,
    version: 1,
    strategy: "single-pass",
    ci_fail_on: "critical",
    repo_intel: true,
  };
}

const SECURITY = agent("a1", "Security");
const STYLE = agent("a2", "Style");
const LEGACY = agent("a3", "Legacy", false);
const ARCHIVE = agent("a4", "Archive", false);

describe("AgentChips", () => {
  it("shows check/+ states, the skipped note, hides deleted ids and drops them from the next change (AC18, EC10, A7)", async () => {
    const onChange = vi.fn();
    const { user } = renderWithProviders(
      <AgentChips
        agents={[SECURITY, STYLE, LEGACY, ARCHIVE]}
        selected={["a1", "a3", "deleted-id"]}
        onChange={onChange}
      />,
    );

    const security = screen.getByRole("button", { name: "Security" });
    const style = screen.getByRole("button", { name: "Style" });
    const legacy = screen.getByRole("button", { name: /Legacy/ });
    expect(security).toHaveAttribute("aria-pressed", "true");
    expect(style).toHaveAttribute("aria-pressed", "false");
    expect(legacy).toHaveAttribute("aria-pressed", "true");
    expect(legacy).toHaveTextContent("skipped — disabled");
    expect(screen.queryByText(/deleted-id/)).not.toBeInTheDocument();
    expect(screen.getAllByRole("button")).toHaveLength(4);
    // an unselected disabled agent is not selectable
    const archive = screen.getByRole("button", { name: "Archive" });
    expect(archive).toBeDisabled();
    expect(archive).not.toHaveTextContent("skipped");
    expect(security).toBeEnabled();
    expect(legacy).toBeEnabled();

    await user.click(archive);
    expect(onChange).not.toHaveBeenCalled();

    // selecting keeps agents-list order and drops the deleted id
    await user.click(style);
    expect(onChange).toHaveBeenLastCalledWith(["a1", "a2", "a3"]);

    // a selected disabled agent can be deselected
    await user.click(legacy);
    expect(onChange).toHaveBeenLastCalledWith(["a1"]);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("refuses the 21st agent with 'Up to 20 agents' and clears the notice on the next change (EC19)", async () => {
    const agents = Array.from({ length: 21 }, (_, i) => agent(`id-${i}`, `Agent ${i}`));
    const selected = agents.slice(0, 20).map((a) => a.id);
    const onChange = vi.fn();
    const { user } = renderWithProviders(<AgentChips agents={agents} selected={selected} onChange={onChange} />);

    await user.click(screen.getByRole("button", { name: "Agent 20" }));
    expect(onChange).not.toHaveBeenCalled();
    expect(screen.getByRole("alert")).toHaveTextContent("Up to 20 agents");

    await user.click(screen.getByRole("button", { name: "Agent 0" }));
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onChange.mock.lastCall?.[0]).toHaveLength(19);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("does not count ids of deleted agents against the limit (EC10, EC19)", async () => {
    const agents = [SECURITY, STYLE];
    const selected = ["a1", ...Array.from({ length: 25 }, (_, i) => `gone-${i}`)];
    const onChange = vi.fn();
    const { user } = renderWithProviders(<AgentChips agents={agents} selected={selected} onChange={onChange} />);

    await user.click(screen.getByRole("button", { name: "Style" }));
    expect(onChange).toHaveBeenCalledWith(["a1", "a2"]);
  });

  it("disables every chip while saving", () => {
    renderWithProviders(<AgentChips agents={[SECURITY, LEGACY]} selected={["a3"]} disabled onChange={vi.fn()} />);
    for (const button of screen.getAllByRole("button")) expect(button).toBeDisabled();
  });

  it("with no agents offers 'Create an agent' linking to /agents (AC18)", () => {
    renderWithProviders(<AgentChips agents={[]} selected={["gone"]} onChange={vi.fn()} />);
    expect(screen.getByText("No agents yet")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Create an agent" })).toHaveAttribute("href", "/agents");
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });

  it("renders no hard-coded copy under pseudo messages (AC21)", async () => {
    const { container } = renderWithProviders(
      <AgentChips agents={[SECURITY, LEGACY]} selected={["a3"]} onChange={vi.fn()} />,
      { messages: pseudoMessages() },
    );
    expect(await screen.findByText(/autoReviewAgents\.skippedDisabled/)).toBeInTheDocument();
    // the agent names are data
    expect(strayText(container, ["Security", "Legacy"])).toEqual([]);
  });
});
