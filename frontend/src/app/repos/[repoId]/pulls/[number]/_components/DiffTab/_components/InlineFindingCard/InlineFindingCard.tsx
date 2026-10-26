/* InlineFindingCard — the FindingCard from Agent runs, reused verbatim under a
   diff line (server/specs/06-smart-diff.md): same severity/title/rationale,
   same Accept/Dismiss wired to the same mutation. Adds "Post to PR": the
   finding becomes an inline GitHub review comment on its line (GitHub only: a GitLab
   PR gets no posting until slice B). */
"use client";

import type { FindingRecord } from "@devdigest/shared";
import { isGitLab } from "@/lib/code-host";
import { useLinkRepo } from "@/lib/link-repo";
import { useCreatePrComment, useFindingAction, usePrComments } from "@/lib/hooks/reviews";
import { FindingCard } from "../../../FindingCard";
import { findPublishedComment, findingCommentInput } from "./helpers";

export function InlineFindingCard({
  f,
  prId,
  canPublish,
  headSha,
}: {
  f: FindingRecord;
  prId: string;
  /** Open PR and the card sits on a diff line — GitHub can anchor a comment there. */
  canPublish?: boolean;
  headSha?: string | null;
}) {
  const action = useFindingAction(prId);
  const { data: comments } = usePrComments(prId);
  const publish = useCreatePrComment(prId);
  const repo = useLinkRepo();
  // No posting on a GitLab PR, nor while the repository (and so its host) is still unknown (FR45).
  const postable = repo != null && !isGitLab(repo);
  // The mutation result bridges the gap until the refetched comment list has it.
  const publishedUrl = findPublishedComment(comments, f.id)?.html_url ?? publish.data?.html_url ?? null;
  return (
    <FindingCard
      f={f}
      defaultExpanded
      pending={action.isPending}
      headSha={headSha}
      onAction={(act) => action.mutate({ findingId: f.id, action: act })}
      publish={
        postable
          ? {
              url: publishedUrl,
              pending: publish.isPending,
              onPost: canPublish ? () => publish.mutate(findingCommentInput(f)) : undefined,
            }
          : undefined
      }
    />
  );
}
