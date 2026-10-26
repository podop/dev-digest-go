/* ProviderName — the display name of a provider id (custom model providers, FR20/EC22): a built-in's
   name, or a custom provider's CURRENT name, so a rename shows everywhere. An undefined custom id
   (deleted, or still loading) and a failed lookup show the raw id. A long name is clipped with an
   ellipsis and carries the full text as a native title. */
"use client";

import { useCustomProviders } from "@/lib/hooks";
import { providerName } from "@/lib/providers/custom";
import { s } from "./styles";

export function ProviderName({ id }: { id: string }) {
  const { data: custom } = useCustomProviders();
  const name = providerName(id, custom);
  return (
    <span style={s.name} title={name}>
      {name}
    </span>
  );
}
