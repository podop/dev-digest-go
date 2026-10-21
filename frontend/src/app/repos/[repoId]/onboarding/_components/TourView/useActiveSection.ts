/* useActiveSection — which tour section is "in view": driven by an IntersectionObserver when the
   browser has one, and set explicitly by the TOC. Also honours a `#section` in the URL once the
   section cards are mounted (a shared link). */
"use client";

import React from "react";
import type { OnboardingSectionId } from "@devdigest/shared/constants/onboarding";
import { OBSERVER_ROOT_MARGIN } from "./constants";
import { firstVisible, sectionFromHash } from "./helpers";

function scrollToSection(id: string): void {
  // jsdom has no scrollIntoView.
  document.getElementById(id)?.scrollIntoView?.({ behavior: "smooth", block: "start" });
}

export function useActiveSection(ids: readonly OnboardingSectionId[], enabled: boolean) {
  const [active, setActive] = React.useState<OnboardingSectionId>(ids[0]!);
  const hashHandled = React.useRef(false);

  React.useEffect(() => {
    if (!enabled) return;
    if (!hashHandled.current) {
      hashHandled.current = true;
      const fromHash = sectionFromHash(ids, window.location.hash);
      if (fromHash) {
        setActive(fromHash);
        scrollToSection(fromHash);
      }
    }
    if (typeof IntersectionObserver === "undefined") return;

    const visible = new Set<string>();
    const observer = new IntersectionObserver(
      (entries) => {
        for (const e of entries) {
          if (e.isIntersecting) visible.add(e.target.id);
          else visible.delete(e.target.id);
        }
        const first = firstVisible(ids, visible);
        if (first) setActive(first);
      },
      { rootMargin: OBSERVER_ROOT_MARGIN },
    );
    for (const id of ids) {
      const el = document.getElementById(id);
      if (el) observer.observe(el);
    }
    return () => observer.disconnect();
  }, [enabled, ids]);

  /** TOC click: scroll to the card and mark it active. */
  const goTo = React.useCallback((id: OnboardingSectionId) => {
    setActive(id);
    scrollToSection(id);
  }, []);

  return { active, goTo };
}
