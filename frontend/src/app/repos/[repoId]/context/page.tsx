import type { Metadata } from "next";
import { ContextView, parseContextSearch } from "./_components/ContextView";

export const metadata: Metadata = { title: "Project Context" };

type Props = {
  params: Promise<{ repoId: string }>;
  searchParams: Promise<{ doc?: string | string[]; mode?: string | string[] }>;
};

/* Route: /repos/:repoId/context — manage the DevDigest store files and browse the
   repo's spec / doc / insight markdown files. Thin entry: resolves the repo id, ?doc=
   and ?mode=, the screen lives in _components/ContextView (keyed by repo, so a late
   write result never lands in the next repo's view). */
export default async function ContextPage({ params, searchParams }: Props) {
  const [{ repoId }, search] = await Promise.all([params, searchParams]);
  return <ContextView key={repoId} repoId={repoId} {...parseContextSearch(search)} />;
}
