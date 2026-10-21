/* leave-guard.tsx — confirm before the user leaves a page with unsaved edits.
   `useLeaveGuard(dirty, message)` is called by the screen that owns the draft; it also
   catches reloads and in-app link clicks. The App Shell navigates with `router.push`,
   which a link-capture guard cannot see, so it asks `useConfirmLeave()` first. */
"use client";

import React from "react";

interface Guard {
  message: string;
}

interface LeaveGuardApi {
  /** Sets the active guard; the returned function clears it (only if it is still the active one). */
  register: (guard: Guard) => () => void;
  /** True when no guard is set, else the user's answer to the guard's confirm dialog. */
  confirmLeave: () => boolean;
}

const NO_GUARD: LeaveGuardApi = { register: () => () => undefined, confirmLeave: () => true };

const LeaveGuardContext = React.createContext<LeaveGuardApi>(NO_GUARD);

export function LeaveGuardProvider({ children }: { children: React.ReactNode }) {
  const active = React.useRef<Guard | null>(null);
  const [api] = React.useState<LeaveGuardApi>(() => ({
    register: (guard) => {
      active.current = guard;
      return () => {
        if (active.current === guard) active.current = null;
      };
    },
    confirmLeave: () => {
      const guard = active.current;
      return guard === null || window.confirm(guard.message);
    },
  }));
  return <LeaveGuardContext.Provider value={api}>{children}</LeaveGuardContext.Provider>;
}

/** Returns a function that is true when leaving is fine (no dirty guard, or the user confirmed). */
export function useConfirmLeave(): () => boolean {
  return React.useContext(LeaveGuardContext).confirmLeave;
}

/** Internal link that leaves the current page (not a new tab, download or a same-path switch). */
function leavingHref(e: MouseEvent): boolean {
  if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return false;
  const a = (e.target as Element | null)?.closest?.("a[href]") as HTMLAnchorElement | null;
  if (!a || a.target === "_blank" || a.hasAttribute("download")) return false;
  const url = new URL(a.href, window.location.href);
  return url.origin === window.location.origin && url.pathname !== window.location.pathname;
}

/** While `dirty`: registers the guard for shell navigation, warns on reload/close
 *  (beforeunload) and confirms before an in-app link navigates away (capture phase,
 *  before Next's <Link> sees the click). */
export function useLeaveGuard(dirty: boolean, message: string): void {
  const { register } = React.useContext(LeaveGuardContext);
  React.useEffect(() => {
    if (!dirty) return;
    const unregister = register({ message });
    const onBeforeUnload = (e: BeforeUnloadEvent) => {
      e.preventDefault();
      e.returnValue = "";
    };
    const onClick = (e: MouseEvent) => {
      if (!leavingHref(e) || window.confirm(message)) return;
      e.preventDefault();
      e.stopPropagation();
    };
    window.addEventListener("beforeunload", onBeforeUnload);
    document.addEventListener("click", onClick, true);
    return () => {
      unregister();
      window.removeEventListener("beforeunload", onBeforeUnload);
      document.removeEventListener("click", onClick, true);
    };
  }, [dirty, message, register]);
}
