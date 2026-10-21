/* BlastTree — expand/collapse rows, caller deep-links, endpoint vs cron chips. */
import { describe, it, expect, afterEach } from "vitest";
import type { BlastCaller, DownstreamImpact } from "@devdigest/shared";
import { renderWithProviders, screen, cleanup, within } from "@/test/render";
import { BlastTree } from "./BlastTree";
import { TREE_INITIAL_SYMBOLS } from "./constants";

const DOWNSTREAM: DownstreamImpact[] = [
  {
    symbol: "rateLimit",
    callers: [
      { name: "publicRouter", file: "src/router.ts", line: 23 },
      { name: "sweep", file: "src/cron.ts", line: 5 },
    ],
    endpoints_affected: ["GET /public"],
    crons_affected: ["nightly-sweep"],
  },
  {
    symbol: "audit",
    callers: [{ name: "logRoute", file: "src/log.ts", line: 9 }],
    endpoints_affected: [],
    crons_affected: [],
  },
];

const href = (c: BlastCaller) => `https://github.com/acme/api/blob/abc/${c.file}#L${c.line}`;

afterEach(cleanup);

describe("BlastTree", () => {
  it("opens the first symbol, lists callers as new-tab links, separates endpoint and cron chips, and toggles rows", async () => {
    const { user } = renderWithProviders(<BlastTree downstream={DOWNSTREAM} callerHref={href} />);

    const first = screen.getByRole("button", { name: /rateLimit\(\)/ });
    const second = screen.getByRole("button", { name: /audit\(\)/ });
    expect(first).toHaveAttribute("aria-expanded", "true");
    expect(second).toHaveAttribute("aria-expanded", "false");
    expect(within(first).getByText("2 callers")).toBeInTheDocument();
    expect(within(second).getByText("1 caller")).toBeInTheDocument();

    const link = screen.getByRole("link", { name: "src/router.ts:23" });
    expect(link).toHaveAttribute("href", "https://github.com/acme/api/blob/abc/src/router.ts#L23");
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", expect.stringContaining("noopener"));
    expect(link).toHaveAttribute("rel", expect.stringContaining("noreferrer"));
    expect(link).toHaveAttribute("title", "publicRouter()");
    expect(screen.queryByText("publicRouter()")).toBeNull();

    expect(within(screen.getByRole("list", { name: "Affected endpoints" })).getByText("GET /public")).toBeInTheDocument();
    expect(within(screen.getByRole("list", { name: "Affected cron jobs" })).getByText("nightly-sweep")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "src/log.ts:9" })).toBeNull();

    await user.click(second);
    expect(second).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("link", { name: "src/log.ts:9" })).toBeInTheDocument();

    await user.click(first);
    expect(first).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("link", { name: "src/router.ts:23" })).toBeNull();
  });

  it("renders a caller whose name is only its file name without ()", () => {
    const fileCaller: DownstreamImpact[] = [
      {
        symbol: "rateLimit",
        callers: [{ name: "composition.test.ts", file: "server/test/composition.test.ts", line: 9 }],
        endpoints_affected: [],
        crons_affected: [],
      },
    ];
    renderWithProviders(<BlastTree downstream={fileCaller} callerHref={href} />);
    expect(screen.getByRole("link", { name: "server/test/composition.test.ts:9" })).toBeInTheDocument();
    expect(screen.queryByText("composition.test.ts()")).toBeNull();
    expect(screen.queryByText("composition.test.ts")).toBeNull();
    expect(screen.getByRole("link", { name: "server/test/composition.test.ts:9" })).not.toHaveAttribute("title");
  });

  it("renders a caller as plain text when there is no link", () => {
    renderWithProviders(<BlastTree downstream={DOWNSTREAM} callerHref={() => undefined} />);
    expect(screen.getByText("src/router.ts:23")).toBeInTheDocument();
    expect(screen.queryByRole("link")).toBeNull();
  });

  it("shows only the first symbols of a long tree behind a Show more / Show fewer toggle", async () => {
    const total = TREE_INITIAL_SYMBOLS + 5;
    const long: DownstreamImpact[] = Array.from({ length: total }, (_, i) => ({
      symbol: `sym${i}`,
      callers: [{ name: `caller${i}`, file: `src/c${i}.ts`, line: i + 1 }],
      endpoints_affected: [],
      crons_affected: [],
    }));
    const { user } = renderWithProviders(<BlastTree downstream={long} callerHref={href} />);

    expect(screen.getAllByRole("button", { name: /sym\d+\(\)/ })).toHaveLength(TREE_INITIAL_SYMBOLS);
    expect(screen.queryByRole("button", { name: /sym10\(\)/ })).toBeNull();
    const more = screen.getByRole("button", { name: "Show 5 more symbols" });
    expect(more).toHaveAttribute("aria-expanded", "false");

    await user.click(more);
    expect(screen.getAllByRole("button", { name: /sym\d+\(\)/ })).toHaveLength(total);
    const fewer = screen.getByRole("button", { name: "Show fewer" });
    expect(fewer).toHaveAttribute("aria-expanded", "true");

    await user.click(fewer);
    expect(screen.getAllByRole("button", { name: /sym\d+\(\)/ })).toHaveLength(TREE_INITIAL_SYMBOLS);
    expect(screen.getByRole("button", { name: "Show 5 more symbols" })).toBeInTheDocument();
  });

  it("has no Show more button when the tree fits", () => {
    renderWithProviders(<BlastTree downstream={DOWNSTREAM} callerHref={href} />);
    expect(screen.queryByRole("button", { name: /Show/ })).toBeNull();
  });
});
