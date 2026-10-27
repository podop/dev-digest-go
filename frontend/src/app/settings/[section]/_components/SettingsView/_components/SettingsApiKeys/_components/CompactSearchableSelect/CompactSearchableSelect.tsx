"use client";

import type React from "react";
import { SearchableSelect } from "@devdigest/ui";

/* The vendored SearchableSelect has no size option and vendor/ui is not edited, so its trigger (the wrapper's
   first child) is brought to the prototype's SelectInput size (8px 11px, 13px) by a rule scoped to this class. */
const CLASS = "dd-compact-searchable";
const CSS = `.${CLASS}>div>div:first-child{padding:8px 11px!important;gap:8px!important}.${CLASS}>div>div:first-child>span{font-size:13px!important}`;

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
