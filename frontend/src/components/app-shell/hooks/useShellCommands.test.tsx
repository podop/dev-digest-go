import React from "react";
import { describe, it, expect, afterEach, vi } from "vitest";
import { renderHook, cleanup, TestProviders, createTestQueryClient } from "@/test/render";
import { LeaveGuardProvider, useLeaveGuard } from "@/lib/leave-guard";

const push = vi.fn();
vi.mock("next/navigation", () => ({ useRouter: () => ({ push }), usePathname: () => "/" }));

import { useShellCommands } from "./useShellCommands";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  push.mockClear();
});

function useShellWithGuard(dirty: boolean) {
  useLeaveGuard(dirty, "Discard unsaved changes?");
  return useShellCommands();
}

const wrapper = ({ children }: { children: React.ReactNode }) => (
  <TestProviders client={createTestQueryClient()}>
    <LeaveGuardProvider>{children}</LeaveGuardProvider>
  </TestProviders>
);

describe("shell navigation and the leave guard", () => {
  it("a dirty editor makes 'Go to' ask first; a refusal keeps the page, a clean page passes", () => {
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    const dirty = renderHook(() => useShellWithGuard(true), { wrapper });
    const settings = dirty.result.current.find((c) => c.id === "settings")!;

    settings.run();
    expect(confirm).toHaveBeenCalledOnce();
    expect(push).not.toHaveBeenCalled();

    confirm.mockReturnValue(true);
    settings.run();
    expect(push).toHaveBeenCalledOnce();
    cleanup();
    push.mockClear();
    confirm.mockClear();

    const clean = renderHook(() => useShellWithGuard(false), { wrapper });
    clean.result.current.find((c) => c.id === "settings")!.run();
    expect(push).toHaveBeenCalledOnce();
    expect(confirm).not.toHaveBeenCalled();
  });
});
