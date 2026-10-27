/* InlineFindingCard — the FindingCard from Agent runs, reused verbatim under a
   diff line (server/specs/06-smart-diff.md): same severity/title/rationale,
   same Accept/Dismiss wired to the same mutation. Adds "Post to PR" ("Post to MR"
   on GitLab): the finding becomes an inline review comment / discussion on its line
   (not for a GitLab repository without a connection). */
"use client";

import { useTranslations } from "next-intl";
import type { FindingRecord } from "@devdigest/shared";
import { isGitLab, isNotConnected } from "@/lib/code-host";
import { commentErrorMessage } from "@/lib/code-host/comment-errors";
import { useActiveRepo } from "@/lib/repo-context";
import { notify } from "@/lib/toast";
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
  const t = useTranslations("prReview");
  const tErr = useTranslations("prCommentErrors");
  const { activeRepo: repo } = useActiveRepo();
  const gitlab = isGitLab(repo);
  const publish = useCreatePrComment(prId, { gitlab });
  // No posting while the repository (and so its host) is still unknown, nor for a GitLab repository without a
  // connection (FR21).
  const postable = repo != null && !isNotConnected(repo);
  // A GitLab post is quiet in the mutation, so this is its one toast (FR20); GitHub keeps the global one.
  const onPost = () =>
    publish.mutate(findingCommentInput(f, { gitlab }), {
      onError: gitlab
        ? (err) => notify.error(commentErrorMessage(err, tErr) ?? t("diff.postFailedGitLab"))
        : undefined,
    });
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
              onPost: canPublish ? onPost : undefined,
            }
          : undefined
      }
    />
  );
}
