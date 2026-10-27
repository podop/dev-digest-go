"use client";

import React from "react";
import { Icon } from "@devdigest/ui";
import { s, type CalloutTone } from "./styles";

type IconName = keyof typeof Icon;

/** Section heading + lede (prototype: h2 18/700, lede 13 secondary). */
export function SectionHeader({ title, body }: { title: React.ReactNode; body?: React.ReactNode }) {
  return (
    <>
      <h2 style={s.h2}>{title}</h2>
      {body && <p style={s.lede}>{body}</p>}
    </>
  );
}

/** A section pane: the prototype caps each section's width (640 by default). */
export function SectionPane({ maxWidth, children }: { maxWidth?: number; children: React.ReactNode }) {
  return <div style={s.section(maxWidth)}>{children}</div>;
}

/**
 * The prototype FormField (label 12.5/600, hint 11.5 muted, 18px apart). Unlike the vendored one it
 * ties the label to its control: pass `htmlFor` with the control's id, or leave it out for a group.
 */
export function Field({
  label,
  htmlFor,
  hint,
  right,
  required,
  children,
  style,
}: {
  label?: React.ReactNode;
  htmlFor?: string;
  hint?: React.ReactNode;
  right?: React.ReactNode;
  required?: boolean;
  children?: React.ReactNode;
  style?: React.CSSProperties;
}) {
  return (
    <div style={{ ...s.field, ...style }}>
      {(label || right) && (
        <div style={s.fieldHead}>
          {label && (
            <label htmlFor={htmlFor} style={s.label}>
              {label}
              {required && <span style={s.required}>*</span>}
            </label>
          )}
          {right && <div style={s.right}>{right}</div>}
        </div>
      )}
      {children}
      {hint && <div style={s.hint}>{hint}</div>}
    </div>
  );
}

/** Link at the right end of a field label, with the prototype's trailing arrow. */
export function HeadLink({ href, children }: { href: string; children: React.ReactNode }) {
  return (
    <a href={href} target="_blank" rel="noopener noreferrer" style={s.headLink}>
      {children}
      <Icon.ArrowRight size={11} aria-hidden />
    </a>
  );
}

/** The prototype TextInput (8px 11px, 13px), a real controlled input. */
export function TextField({
  value,
  onChange,
  mono,
  suffix,
  variant,
  invalid,
  ...rest
}: {
  value: string;
  onChange?: (v: string) => void;
  mono?: boolean;
  suffix?: React.ReactNode;
  variant?: "elevated" | "surface";
  invalid?: boolean;
} & Omit<React.InputHTMLAttributes<HTMLInputElement>, "value" | "onChange" | "size">) {
  return (
    <div style={s.inputBox(variant, invalid)}>
      <input
        {...rest}
        className={mono ? "mono" : undefined}
        value={value}
        aria-invalid={invalid || undefined}
        onChange={(e) => onChange?.(e.target.value)}
        // `.mono` sets the family; a plain field takes the page font instead of the UA input font.
        style={mono ? s.input : { ...s.input, fontFamily: "inherit" }}
      />
      {suffix}
    </div>
  );
}

/** Grey info box with an Info icon (the note at the bottom of most sections). */
export function InfoNote({ children, style }: { children: React.ReactNode; style?: React.CSSProperties }) {
  return (
    <div style={{ ...s.infoNote, ...style }}>
      <Icon.Info size={15} style={s.infoIcon} aria-hidden />
      <span>{children}</span>
    </div>
  );
}

const CALLOUT_ICON: Record<CalloutTone, IconName> = { ok: "CheckCircle", warn: "AlertTriangle", crit: "XCircle" };

/** Tinted result box ("Connection verified · gpt-4.1 reachable · 142ms"). Failure is an alert. */
export function Callout({
  tone,
  icon,
  children,
  style,
}: {
  tone: CalloutTone;
  icon?: IconName | null;
  children: React.ReactNode;
  style?: React.CSSProperties;
}) {
  const I = icon === null ? null : Icon[icon ?? CALLOUT_ICON[tone]];
  return (
    <div role={tone === "crit" ? "alert" : "status"} style={{ ...s.callout(tone), ...style }}>
      {I && <I size={15} style={s.calloutIcon(tone)} aria-hidden />}
      <span style={{ minWidth: 0 }}>{children}</span>
    </div>
  );
}

/** Repository / plugin row. `list` = bordered row in a field list; `card` = row inside an integration card. */
export function ListRow({
  icon = "GitBranch",
  name,
  variant = "list",
  children,
  style,
}: {
  icon?: IconName | null;
  name: React.ReactNode;
  variant?: "list" | "card";
  children?: React.ReactNode;
  style?: React.CSSProperties;
}) {
  const I = icon ? Icon[icon] : null;
  return (
    <div style={{ ...(variant === "card" ? s.cardRow : s.listRow), ...style }}>
      {I && <I size={13} style={s.rowIcon} aria-hidden />}
      <span className="mono" style={s.rowName}>
        {name}
      </span>
      {children}
    </div>
  );
}

/** Muted meta text in a row ("indexed 2m ago"). */
export function RowMeta({ children }: { children: React.ReactNode }) {
  return <span style={s.rowMeta}>{children}</span>;
}

/** The prototype MonoLink (12px mono text button): row actions such as Remove, Manage, Disable. */
export function MonoAction({
  children,
  onClick,
  disabled,
  tone = "default",
  ...rest
}: {
  children: React.ReactNode;
  onClick?: () => void;
  disabled?: boolean;
  tone?: "default" | "crit";
} & Omit<React.ButtonHTMLAttributes<HTMLButtonElement>, "onClick" | "children" | "style">) {
  const [hover, setHover] = React.useState(false);
  return (
    <button
      {...rest}
      type="button"
      className="mono"
      disabled={disabled}
      onClick={onClick}
      onMouseEnter={() => setHover(true)}
      onMouseLeave={() => setHover(false)}
      onFocus={() => setHover(true)}
      onBlur={() => setHover(false)}
      style={{ ...s.monoAction(hover && !disabled, tone), opacity: disabled ? 0.5 : 1 }}
    >
      {children}
    </button>
  );
}

/** The prototype Badge (11.5/600, 2px 8px), optionally with a status dot. */
export function KitBadge({
  children,
  color = "var(--text-muted)",
  bg = "var(--bg-hover)",
  dot,
  mono,
}: {
  children: React.ReactNode;
  color?: string;
  bg?: string;
  dot?: boolean;
  mono?: boolean;
}) {
  return (
    <span className={mono ? "mono" : undefined} style={s.badge(color, bg)}>
      {dot && <span aria-hidden style={s.badgeDot(color)} />}
      {children}
    </span>
  );
}

/** The prototype Chip (4px 10px, 12.5px) as a toggle button. */
export function KitChip({
  active,
  icon,
  children,
  onClick,
  disabled,
  ...rest
}: {
  active?: boolean;
  icon?: IconName;
  children: React.ReactNode;
  onClick?: () => void;
  disabled?: boolean;
} & Omit<React.ButtonHTMLAttributes<HTMLButtonElement>, "onClick" | "children" | "style">) {
  const [hover, setHover] = React.useState(false);
  const I = icon ? Icon[icon] : null;
  return (
    <button
      {...rest}
      type="button"
      disabled={disabled}
      onClick={onClick}
      onMouseEnter={() => setHover(true)}
      onMouseLeave={() => setHover(false)}
      style={{ ...s.chip(!!active, hover && !disabled), opacity: disabled ? 0.6 : 1 }}
    >
      {I && <I size={13} aria-hidden />}
      {children}
    </button>
  );
}

/** Integration card: 34px icon tile, title, description, optional status at the right. */
export function SettingsCard({
  icon,
  title,
  desc,
  status,
  children,
  headingLevel = 3,
}: {
  icon: IconName;
  title: React.ReactNode;
  desc?: React.ReactNode;
  status?: React.ReactNode;
  children?: React.ReactNode;
  headingLevel?: 3 | 4;
}) {
  const I = Icon[icon];
  const H = headingLevel === 3 ? "h3" : "h4";
  return (
    <section style={s.card}>
      <div style={s.cardHead}>
        <div style={s.cardIcon} aria-hidden>
          <I size={18} />
        </div>
        <div style={{ flex: 1, minWidth: 0 }}>
          <H style={s.cardTitle}>{title}</H>
          {desc && <div style={s.cardDesc}>{desc}</div>}
        </div>
        {status}
      </div>
      {children}
    </section>
  );
}

/** Status text placed inside an input box ("✓ Connected"). */
export function InlineStatus({ tone, children }: { tone: "ok" | "crit" | "muted"; children: React.ReactNode }) {
  const color = tone === "muted" ? "var(--text-muted)" : `var(--${tone})`;
  const I = tone === "ok" ? Icon.CheckCircle : tone === "crit" ? Icon.XCircle : null;
  return (
    <span style={s.inlineStatus(color)}>
      {I && <I size={13} aria-hidden />}
      {children}
    </span>
  );
}

/** The reveal toggle at the end of a secret input (Eye / EyeOff, 14px muted). */
export function RevealButton({
  revealed,
  onToggle,
  label,
}: {
  revealed: boolean;
  onToggle: () => void;
  label: string;
}) {
  return (
    <button type="button" style={s.iconButton} aria-label={label} aria-pressed={revealed} onClick={onToggle}>
      {revealed ? <Icon.Eye size={14} /> : <Icon.EyeOff size={14} />}
    </button>
  );
}
