import { describe, it, expect, afterEach } from "vitest";
import { renderWithProviders, screen, cleanup, within } from "@/test/render";
import { finding } from "../../fixtures";
import { TopFindings } from "./TopFindings";

afterEach(cleanup);

describe("TopFindings", () => {
  it("lists up to 3 findings with severity · title, file:line, agent and a state badge", () => {
    renderWithProviders(
      <TopFindings
        findings={[
          finding({ finding_id: "f1", severity: "CRITICAL", title: "SQL injection", state: "open" }),
          finding({ finding_id: "f2", severity: "WARNING", title: "Unbounded retry", file: "pkg/retry.go", start_line: 12, agent_name: "Perf agent", state: "accepted" }),
          finding({ finding_id: "f3", severity: "SUGGESTION", title: "Rename helper", state: "dismissed" }),
        ]}
      />,
    );
    const items = within(screen.getByRole("list", { name: "Top findings" })).getAllByRole("listitem");
    expect(items).toHaveLength(3);

    const first = within(items[0]!);
    expect(first.getByText("Critical")).toBeInTheDocument();
    expect(first.getByText(/SQL injection/)).toBeInTheDocument();
    expect(first.getByText("internal/billing/invoice.go:88")).toBeInTheDocument();
    expect(first.getByText("Security agent")).toBeInTheDocument();
    expect(first.getByText("open")).toHaveStyle({ color: "var(--warn)" });

    const second = within(items[1]!);
    expect(second.getByText("Warning")).toBeInTheDocument();
    expect(second.getByText("pkg/retry.go:12")).toBeInTheDocument();
    expect(second.getByText("Perf agent")).toBeInTheDocument();
    expect(second.getByText("accepted")).toHaveStyle({ color: "var(--ok)" });

    expect(within(items[2]!).getByText("Suggestion")).toBeInTheDocument();
    expect(within(items[2]!).getByText("dismissed")).toHaveStyle({ color: "var(--text-muted)" });
  });

  it('says "No notable findings recorded." when there are none', () => {
    renderWithProviders(<TopFindings findings={[]} />);
    expect(screen.getByText("No notable findings recorded.")).toBeInTheDocument();
    expect(screen.queryByRole("list")).not.toBeInTheDocument();
  });
});
