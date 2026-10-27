/* ConnectionRow — the identity line (Slice B FR22, EC27, AC20, AC33) and its no-request rule (A12): the line is
   derived from the test result passed in, so the rows render with no fetch stub at all. */
import { describe, it, expect, afterEach, vi } from "vitest";
import type { ConnTestResult, GitLabConnection } from "@/lib/types";
import { renderWithProviders, screen, cleanup, within, pseudoMessages } from "@/test/render";
import { strayText } from "@/test/pseudo";
import type { TestEntry } from "../../useConnectionTests";
import { ConnectionRow } from "./ConnectionRow";

afterEach(cleanup);

function conn(n: number): GitLabConnection {
  return {
    id: `c${n}`,
    name: `Conn ${n}`,
    base_url: `https://gl${n}.example.com`,
    host: `gl${n}.example.com`,
    token_configured: true,
    repo_count: n,
    created_at: "2026-10-26T00:00:00Z",
    updated_at: "2026-10-26T00:00:00Z",
  };
}

function ok(over: Partial<ConnTestResult> = {}): ConnTestResult {
  return { provider: "gitlab", ok: true, message: "Connected as @ann", login: "ann", token_access: "full", ...over };
}

function rows(entries: (TestEntry | undefined)[], messages?: ReturnType<typeof pseudoMessages>) {
  const noop = vi.fn();
  return renderWithProviders(
    <ul>
      {entries.map((test, i) => {
        const connection = conn(i + 1);
        return <ConnectionRow key={connection.id} connection={connection} test={test} onTest={noop} onEdit={noop} onDelete={noop} />;
      })}
    </ul>,
    messages ? { messages } : undefined,
  );
}

describe("ConnectionRow identity line", () => {
  it("reads the login after an ok test, the fallback before one and the read-only notice for a read_only token (AC20, FR22, EC27)", () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch");
    const { container } = rows([
      { pending: false, result: ok() },
      undefined,
      { pending: false, result: ok({ token_access: "read_only" }) },
      { pending: false, result: { provider: "gitlab", ok: false, message: "GitLab rejected the token (401)" } },
      { pending: true, result: null },
    ]);

    // The rows are the top-level items; the test facts nest list items of their own.
    const items = Array.from(container.querySelector("ul")!.children) as HTMLElement[];
    expect(within(items[0]!).getByText("Notes are posted as @ann.")).toBeInTheDocument();
    expect(within(items[1]!).getByText("Notes are posted as the token's user.")).toBeInTheDocument();
    expect(within(items[2]!).getByText("This token is read-only and cannot post notes.")).toBeInTheDocument();
    expect(within(items[2]!).queryByText(/Notes are posted as @/)).not.toBeInTheDocument();
    // A failed test and a test in flight say nothing about the user: the fallback stays.
    expect(within(items[3]!).getByText("Notes are posted as the token's user.")).toBeInTheDocument();
    expect(within(items[4]!).getByText("Notes are posted as the token's user.")).toBeInTheDocument();
    // A12: the line comes from the result in memory.
    expect(fetchSpy).not.toHaveBeenCalled();
    fetchSpy.mockRestore();
  });

  it("renders the identity line from messages only (AC33)", () => {
    const { container } = rows(
      [{ pending: false, result: ok() }, undefined, { pending: false, result: ok({ token_access: "read_only" }) }],
      pseudoMessages(),
    );
    expect(screen.getAllByText(/⟦gitlabIntegration\.row\.identity/)).toHaveLength(3);
    expect(strayText(container, [/^Conn \d$/, /^https:\/\/gl\d\.example\.com$/])).toEqual([]);
  });
});
