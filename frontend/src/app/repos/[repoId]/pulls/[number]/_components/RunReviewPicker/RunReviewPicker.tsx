/* RunReviewPicker — the PR page "Run Review" control (specs/multi-agent-review.md FR12/FR13).
   A multi-select popover over the agents with their estimated time. One checked agent runs
   in place (hands the run ids up for live SSE status); 2+ start a multi-agent run and
   navigate to its results page. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { Button, Checkbox, Icon } from "@devdigest/ui";
import { NO_ESTIMATE } from "./constants";
import { estimateSeconds } from "./helpers";
import { s } from "./styles";
import { useRunReviewPicker } from "./useRunReviewPicker";

export function RunReviewPicker({
  prId,
  size = "sm",
  kind = "primary",
  warnMerged = false,
  onRunStart,
  onRunsStarted,
  onRunSettled,
}: {
  prId: string;
  size?: "sm" | "md" | "lg";
  kind?: "primary" | "secondary";
  /** PR is already merged/closed — dim the trigger and warn, but still allow. */
  warnMerged?: boolean;
  /** Fired the moment a single-agent run is kicked off (before it completes). */
  onRunStart?: () => void;
  onRunsStarted?: (runIds: string[]) => void;
  /** Fired when the run request settles (success or error). */
  onRunSettled?: () => void;
}) {
  const t = useTranslations("prReview");
  const p = useRunReviewPicker({ prId, onRunStart, onRunsStarted, onRunSettled });
  const { open, setOpen } = p;
  const rootRef = React.useRef<HTMLDivElement>(null);

  React.useEffect(() => {
    if (!open) return;
    const onMouseDown = (e: MouseEvent) => {
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) setOpen(false);
    };
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    document.addEventListener("mousedown", onMouseDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("mousedown", onMouseDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [open, setOpen]);

  const count = p.selectedIds.length;

  return (
    <div ref={rootRef} style={s.root}>
      <span
        title={warnMerged ? t("runReview.mergedTooltip") : undefined}
        style={warnMerged ? { opacity: 0.6 } : undefined}
      >
        <Button
          kind={kind}
          size={size}
          iconRight="ChevronDown"
          icon="Sparkles"
          loading={p.isPending}
          aria-haspopup="dialog"
          aria-expanded={open}
          onClick={() => setOpen(!open)}
        >
          {p.isPending ? t("runReview.running") : t("runReview.runReview")}
        </Button>
      </span>
      {open && (
        <div role="dialog" aria-label={t("runReview.pickerLabel")} style={s.popover}>
          {warnMerged && (
            <div style={s.warning}>
              <Icon.AlertTriangle size={13} style={{ color: "var(--warn)", flexShrink: 0 }} />
              <span>{t("runReview.mergedWarning")}</span>
            </div>
          )}
          {p.agents.length === 0 ? (
            <button type="button" style={s.empty} onClick={p.goToAgents}>
              {t("runReview.noAgents")}
            </button>
          ) : (
            <>
              <div style={s.toolbar}>
                <Button kind="ghost" size="sm" onClick={p.selectAll}>
                  {t("runReview.selectAll")}
                </Button>
                <Button kind="ghost" size="sm" onClick={p.clear} disabled={count === 0}>
                  {t("runReview.clear")}
                </Button>
              </div>
              <div style={s.list}>
                {p.agents.map((a) => {
                  const seconds = estimateSeconds(p.estimates.get(a.id));
                  return (
                    <div key={a.id} style={s.row}>
                      <Checkbox
                        checked={p.selected.has(a.id)}
                        onChange={() => p.toggle(a.id)}
                        label={
                          <span style={s.rowLabel}>
                            <span style={s.rowName}>{a.name}</span>
                            <span style={s.rowHint}>
                              {a.enabled ? a.model : t("runReview.agentDisabled", { model: a.model })}
                            </span>
                          </span>
                        }
                      />
                      <span className="mono tnum" style={s.estimate}>
                        {seconds === null ? NO_ESTIMATE : t("runReview.estimate", { seconds })}
                      </span>
                    </div>
                  );
                })}
              </div>
            </>
          )}
          <div style={s.footer}>
            <Button kind="primary" size="md" full disabled={count === 0 || p.isPending} onClick={p.start}>
              {count >= 2 ? t("runReview.runMany", { count }) : t("runReview.runOne")}
            </Button>
            <Button kind="ghost" size="sm" icon="Settings" onClick={p.configure}>
              {t("runReview.configureAgents")}
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}
