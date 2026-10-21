/* EstimateNote — the one note under every cost breakdown: costs are DevDigest estimates. */
"use client";

import React from "react";
import { useTranslations } from "next-intl";
import { s } from "./styles";

export function EstimateNote() {
  const t = useTranslations("agentPerformance.breakdown");
  return <p style={s.note}>{t("estimateNote")}</p>;
}
