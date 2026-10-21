/* InlineFindingCard — the FindingCard from Agent runs, reused verbatim under a
   diff line (server/specs/06-smart-diff.md): same severity/title/rationale,
   same Accept/Dismiss wired to the same mutation. Adds "Post to PR": the
   finding becomes an inline GitHub review comment on its line. */
"use client";

import type { FindingRecord } from "@devdigest/shared";
import { useCreatePrComment, useFindingAction, usePrComments } from "@/lib/hooks/reviews";
import { FindingCard } from "../../../FindingCard";
import { findPublishedComment, findingCommentInput } from "./helpers";

export function InlineFindingCard({
  f,
  prId,
  canPublish,
  repoFullName,
  headSha,
}: {
  f: FindingRecord;
  prId: string;
  /** Open PR and the card sits on a diff line — GitHub can anchor a comment there. */
  canPublish?: boolean;
  repoFullName?: string | null;
  headSha?: string | null;
}) {
  const action = useFindingAction(prId);
  const { data: comments } = usePrComments(prId);
  const publish = useCreatePrComment(prId);
  // The mutation result bridges the gap until the refetched comment list has it.
  const publishedUrl = findPublishedComment(comments, f.id)?.html_url ?? publish.data?.html_url ?? null;
  return (
    <FindingCard
      f={f}
      defaultExpanded
      pending={action.isPending}
      repoFullName={repoFullName}
      headSha={headSha}
      onAction={(act) => action.mutate({ findingId: f.id, action: act })}
      publish={{
        url: publishedUrl,
        pending: publish.isPending,
        onPost: canPublish ? () => publish.mutate(findingCommentInput(f)) : undefined,
      }}
    />
  );
}
