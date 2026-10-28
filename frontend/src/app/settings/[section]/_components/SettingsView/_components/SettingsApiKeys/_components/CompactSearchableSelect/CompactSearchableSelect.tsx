"use client";

import type React from "react";
import { SearchableSelect } from "@devdigest/ui";

/* The vendored SearchableSelect has no size option and vendor/ui is not edited, so its trigger (the wrapper's
   first child) is brought to the prototype's SelectInput size (8px 11px, 13px) by a rule scoped to this class,
   and so is the open list (user, 2026-10-27: items at the standard size): the search box 13px like the trigger,
   the options 12px — they render in JetBrains Mono (mono is the vendored default), which at 13px reads larger
   than the 13px Inter around it. */
const CLASS = "dd-compact-searchable";
const CSS = [
  `.${CLASS}>div>div:first-child{padding:8px 11px!important;gap:8px!important}`,
  `.${CLASS}>div>div:first-child>span{font-size:13px!important}`,
  `.${CLASS} input{font-size:13px!important}`,
  `.${CLASS} button{font-size:12px!important;padding:6px 10px!important}`,
].join("");

/** SearchableSelect at the prototype's compact size (Default model, Feature Models). */
export function CompactSearchableSelect(props: React.ComponentProps<typeof SearchableSelect>) {
  return (
    <div className={CLASS}>
      <style href={CLASS} precedence="default">
        {CSS}
      </style>
      <SearchableSelect {...props} />
    </div>
  );
}
